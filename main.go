// Copyright 2020 Ohio Supercomputer Center
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/api"
	"github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/model"
	"github.com/prometheus/common/version"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	appName          = "k8-namespace-reaper"
	metricsPath      = "/metrics"
	metricsNamespace = "k8_namespace_reaper"
)

var (
	namespaceLabels   = kingpin.Flag("namespace-labels", "Labels to use when filtering namespaces").Default("").Envar("NAMESPACE_LABELS").String()
	namespacePrefix   = kingpin.Flag("namespace-prefix", "Prefix of namespaces to reap").Default("").Envar("NAMESPACE_PREFIX").String()
	prometheusAddress = kingpin.Flag("prometheus-address", "URL for Prometheus, eg http://prometheus.example.com:9090").Envar("PROMETHEUS_ADDRESS").Required().String()
	prometheusTimeout = kingpin.Flag("prometheus-timeout", "Duration to timeout Prometheus query").Default("30s").Envar("PROMETHEUS_TIMEOUT").Duration()
	reapAfter         = kingpin.Flag("reap-after", "How long to wait before reaping unused namespaces").Default("168h").Envar("REAP_AFTER").Duration()
	interval          = kingpin.Flag("interval", "Duration between reap runs").Default("6h").Envar("INTERLVAL").Duration()
	listenAddress     = kingpin.Flag("listen-address", "Address to listen for HTTP requests").Default(":8080").Envar("LISTEN_ADDRESS").String()
	processMetrics    = kingpin.Flag("process-metrics", "Collect metrics about running process such as CPU and memory and Go stats").Default("true").Envar("PROCESS_METRICS").Bool()
	runOnce           = kingpin.Flag("run-once", "Set application to run once then exit, ie executed with cron").Default("false").Envar("RUN_ONCE").Bool()
	kubeconfig        = kingpin.Flag("kubeconfig", "Path to kubeconfig when running outside Kubernetes cluster").Default("").Envar("KUBECONFIG").String()
	logLevel          = kingpin.Flag("log-level", "Log level, One of: [debug, info, warn, error]").Default("info").Envar("LOG_LEVEL").String()
	logFormat         = kingpin.Flag("log-format", "Log format, One of: [logfmt, json]").Default("logfmt").Envar("LOG_FORMAT").String()
	timestampFormat   = log.TimestampFormat(
		func() time.Time { return time.Now().UTC() },
		"2006-01-02T15:04:05.000Z07:00",
	)
	timeNow         = time.Now
	metricBuildInfo = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "build_info",
		Help:      "Build information",
		ConstLabels: prometheus.Labels{
			"version":   version.Version,
			"revision":  version.Revision,
			"branch":    version.Branch,
			"builddate": version.BuildDate,
			"goversion": version.GoVersion,
		},
	})
	metricReapedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "reaped_total",
		Help:      "Total number of namespaces reaped",
	})
	metricError = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "error",
		Help:      "Indicates an error was encountered",
	})
	metricErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "errors_total",
		Help:      "Total number of errors",
	})
	metricDuration = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "run_duration_seconds",
		Help:      "Last runtime duration in seconds",
	})
)

func init() {
	metricBuildInfo.Set(1)
}

func main() {
	kingpin.Version(version.Print(appName))
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	logger := setupLogging()
	if logger == nil {
		os.Exit(1)
	}

	if err := validateArgs(logger); err != nil {
		os.Exit(1)
	}

	var config *rest.Config
	var err error
	if *kubeconfig == "" {
		level.Info(logger).Log("msg", "Loading in cluster kubeconfig", "kubeconfig", *kubeconfig)
		config, err = rest.InClusterConfig()
	} else {
		level.Info(logger).Log("msg", "Loading kubeconfig", "kubeconfig", *kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}
	if err != nil {
		level.Error(logger).Log("msg", "Error loading kubeconfig", "err", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		level.Error(logger).Log("msg", "Unable to generate Clientset", "err", err)
		os.Exit(1)
	}

	level.Info(logger).Log("msg", fmt.Sprintf("Starting %s", appName), "version", version.Info())
	level.Info(logger).Log("msg", "Build context", "build_context", version.BuildContext())

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
	             <head><title>` + appName + `</title></head>
	             <body>
	             <h1>` + appName + `</h1>
	             <p><a href='` + metricsPath + `'>Metrics</a></p>
	             </body>
	             </html>`))
	})
	http.Handle(metricsPath, promhttp.HandlerFor(metricGathers(), promhttp.HandlerOpts{}))

	go func() {
		if err := http.ListenAndServe(*listenAddress, nil); err != nil {
			level.Error(logger).Log("msg", "Error starting HTTP server", "err", err)
			os.Exit(1)
		}
	}()

	for {
		var errNum int
		err = run(clientset, logger)
		if err != nil {
			errNum = 1
		}
		metricError.Set(float64(errNum))
		if *runOnce {
			os.Exit(errNum)
		} else {
			level.Debug(logger).Log("msg", "Sleeping for interval", "interval", fmt.Sprintf("%.0f", (*interval).Seconds()))
			time.Sleep(*interval)
		}
	}
}

func setupLogging() log.Logger {
	var logger log.Logger
	if *logFormat == "json" {
		logger = log.NewJSONLogger(log.NewSyncWriter(os.Stderr))
	} else {
		logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	}
	switch *logLevel {
	case "debug":
		logger = level.NewFilter(logger, level.AllowDebug())
	case "info":
		logger = level.NewFilter(logger, level.AllowInfo())
	case "warn":
		logger = level.NewFilter(logger, level.AllowWarn())
	case "error":
		logger = level.NewFilter(logger, level.AllowError())
	default:
		logger = level.NewFilter(logger, level.AllowError())
		level.Error(logger).Log("msg", "Unrecognized log level", "level", *logLevel)
		return nil
	}
	logger = log.With(logger, "ts", timestampFormat, "caller", log.DefaultCaller)
	return logger
}

func validateArgs(logger log.Logger) []error {
	var errs []error
	if *namespaceLabels == "" && *namespacePrefix == "" {
		errs = append(errs, errors.New("Must provide either namespaces labels or namespace prefix"))
	}
	for _, err := range errs {
		level.Error(logger).Log("err", err)
	}
	return errs
}

func run(clientset kubernetes.Interface, logger log.Logger) error {
	start := timeNow()
	defer metricDuration.Set(time.Since(start).Seconds())
	namespaces, err := getNamespaces(clientset, logger)
	if err != nil {
		level.Error(logger).Log("msg", "Error getting namespaces", "err", err)
		return err
	}
	activeNamespaces, err := getActiveNamespaces(logger)
	if err != nil {
		level.Error(logger).Log("msg", "Error getting active namespaces", "err", err)
		return err
	}
	errCount := reap(namespaces, activeNamespaces, clientset, logger)
	if errCount > 0 {
		err := fmt.Errorf("%d errors encountered during reap", errCount)
		level.Error(logger).Log("msg", err)
		return err
	}
	return nil
}

func getNamespaces(clientset kubernetes.Interface, logger log.Logger) ([]string, error) {
	var namespaces []string
	nsLabels := strings.Split(*namespaceLabels, ",")
	if len(nsLabels) == 0 {
		nsLabels = []string{"all"}
	}
	for _, label := range nsLabels {
		nsListOptions := metav1.ListOptions{}
		if label != "all" {
			nsListOptions.LabelSelector = label
		}
		level.Debug(logger).Log("msg", "Getting namespaces with label", "label", label)
		ns, err := clientset.CoreV1().Namespaces().List(context.TODO(), nsListOptions)
		if err != nil {
			level.Error(logger).Log("msg", "Error getting namespace list", "label", label, "err", err)
			return nil, err
		}
		level.Debug(logger).Log("msg", "Namespaces returned", "count", len(ns.Items))
		for _, namespace := range ns.Items {
			if *namespacePrefix != "" && !strings.HasPrefix(namespace.Name, *namespacePrefix) {
				level.Debug(logger).Log("msg", "Skipping namespace that does not match namespace prefix", "namespace", namespace.Name)
				continue
			}
			currentAge := timeNow().Sub(namespace.CreationTimestamp.Time)
			if currentAge < *reapAfter {
				level.Debug(logger).Log("msg", "Skipping namespace due to age", "namespace", namespace.Name, "age", currentAge.String())
				continue
			}
			namespaces = append(namespaces, namespace.Name)
		}
	}
	return namespaces, nil
}

func getActiveNamespaces(logger log.Logger) ([]string, error) {
	var namespaces []string
	client, err := api.NewClient(api.Config{
		Address: *prometheusAddress,
	})
	if err != nil {
		level.Error(logger).Log("msg", "Error creating client", "err", err)
		return nil, err
	}

	v1api := v1.NewAPI(client)
	ctx, cancel := context.WithTimeout(context.Background(), *prometheusTimeout)
	defer cancel()
	var queryFilter string
	if *namespacePrefix != "" {
		queryFilter = fmt.Sprintf("{namespace=~\"%s.+\"}", *namespacePrefix)
	}
	query := fmt.Sprintf("max(max_over_time(timestamp(kube_pod_container_info%s)[%s:5m])) by (namespace)",
		queryFilter, (*reapAfter).String())
	result, warnings, err := v1api.Query(ctx, query, time.Now())
	if err != nil {
		level.Error(logger).Log("msg", "Error querying Prometheus", "err", err)
		return nil, err
	}
	for _, warning := range warnings {
		level.Warn(logger).Log("msg", "Warning querying Prometheus", "warning", warning)
	}
	if result.Type() == model.ValVector {
		vector := result.(model.Vector)
		for _, vec := range vector {
			if val, ok := vec.Metric["namespace"]; ok {
				namespaces = append(namespaces, string(val))
			}
		}
	} else {
		level.Error(logger).Log("msg", "Unrecognized result type", "type", result.Type())
		return nil, err
	}
	return namespaces, nil
}

func reap(namespaces []string, activeNamespaces []string, clientset kubernetes.Interface, logger log.Logger) int {
	reaped := 0
	errCount := 0
	for _, namespace := range namespaces {
		namespaceLogger := log.With(logger, "namespace", namespace)
		if sliceContains(activeNamespaces, namespace) {
			level.Debug(namespaceLogger).Log("msg", "Skipping active namespace")
			continue
		}
		level.Info(namespaceLogger).Log("msg", "Reaping namespace")
		err := clientset.CoreV1().Namespaces().Delete(context.TODO(), namespace, metav1.DeleteOptions{})
		if err != nil {
			errCount++
			level.Error(namespaceLogger).Log("msg", "Error deleting namespace", "err", err)
			metricErrorsTotal.Inc()
		} else {
			reaped++
			metricReapedTotal.Inc()
		}
	}
	level.Info(logger).Log("msg", "Reap summary", "namespaces", reaped)
	return errCount
}

func metricGathers() prometheus.Gatherers {
	registry := prometheus.NewRegistry()
	registry.MustRegister(metricBuildInfo)
	registry.MustRegister(metricReapedTotal)
	registry.MustRegister(metricError)
	registry.MustRegister(metricErrorsTotal)
	registry.MustRegister(metricDuration)
	gatherers := prometheus.Gatherers{registry}
	if *processMetrics {
		gatherers = append(gatherers, prometheus.DefaultGatherer)
	}
	return gatherers
}

func sliceContains(slice []string, str string) bool {
	for _, s := range slice {
		if str == s {
			return true
		}
	}
	return false
}
