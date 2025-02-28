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
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/model"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/version"
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
	namespaceLabels             = kingpin.Flag("namespace-labels", "Labels to use when filtering namespaces").Default("").Envar("NAMESPACE_LABELS").String()
	namespaceRegexp             = kingpin.Flag("namespace-regexp", "Regular expression of namespaces to reap").Default("").Envar("NAMESPACE_REGEXP").String()
	namespaceLastUsedAnnotation = kingpin.Flag("namespace-last-used-annotation", "Annotation of when namespace was last used, must be Unix timestamp").Default("").Envar("NAMESPACE_LAST_USED_ANNOTATION").String()
	prometheusAddress           = kingpin.Flag("prometheus-address", "URL for Prometheus, eg http://prometheus:9090").Envar("PROMETHEUS_ADDRESS").Required().String()
	prometheusTimeout           = kingpin.Flag("prometheus-timeout", "Duration to timeout Prometheus query").Default("30s").Envar("PROMETHEUS_TIMEOUT").Duration()
	reapAfter                   = kingpin.Flag("reap-after", "How long to wait before reaping unused namespaces").Default("168h").Envar("REAP_AFTER").Duration()
	lastUsedThreshold           = kingpin.Flag("last-used-threshold", "How long after last used can a namespace be reaped").Default("4h").Envar("LAST_USED_THRESHOLD").Duration()
	interval                    = kingpin.Flag("interval", "Duration between reap runs").Default("6h").Envar("INTERLVAL").Duration()
	listenAddress               = kingpin.Flag("listen-address", "Address to listen for HTTP requests").Default(":8080").Envar("LISTEN_ADDRESS").String()
	processMetrics              = kingpin.Flag("process-metrics", "Collect metrics about running process such as CPU and memory and Go stats").Default("true").Envar("PROCESS_METRICS").Bool()
	runOnce                     = kingpin.Flag("run-once", "Set application to run once then exit, ie executed with cron").Default("false").Envar("RUN_ONCE").Bool()
	kubeconfig                  = kingpin.Flag("kubeconfig", "Path to kubeconfig when running outside Kubernetes cluster").Default("").Envar("KUBECONFIG").String()
	logLevel                    = kingpin.Flag("log-level", "Log level, One of: [debug, info, warn, error]").Default("info").Envar("LOG_LEVEL").Enum(promslog.LevelFlagOptions...)
	logFormat                   = kingpin.Flag("log-format", "Log format, One of: [logfmt, json]").Default("logfmt").Envar("LOG_FORMAT").Enum(promslog.FormatFlagOptions...)
	timeNow                     = time.Now
	metricBuildInfo             = prometheus.NewGauge(prometheus.GaugeOpts{
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
		logger.Info("Loading in cluster kubeconfig", "kubeconfig", *kubeconfig)
		config, err = rest.InClusterConfig()
	} else {
		logger.Info("Loading kubeconfig", "kubeconfig", *kubeconfig)
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}
	if err != nil {
		logger.Error("Error loading kubeconfig", "err", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		logger.Error("Unable to generate Clientset", "err", err)
		os.Exit(1)
	}

	logger.Info(fmt.Sprintf("Starting %s", appName), "version", version.Info())
	logger.Info("Build context", "build_context", version.BuildContext())

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
			logger.Error("Error starting HTTP server", "err", err)
			os.Exit(1)
		}
	}()

	for {
		var errNum int
		start := timeNow()
		err = run(clientset, logger)
		metricDuration.Set(time.Since(start).Seconds())
		if err != nil {
			errNum = 1
		}
		metricError.Set(float64(errNum))
		if *runOnce {
			os.Exit(errNum)
		} else {
			logger.Debug("Sleeping for interval", "interval", fmt.Sprintf("%.0f", (*interval).Seconds()))
			time.Sleep(*interval)
		}
	}
}

func setupLogging() *slog.Logger {
	level := &promslog.AllowedLevel{}
	_ = level.Set(*logLevel)
	format := &promslog.AllowedFormat{}
	_ = format.Set(*logFormat)
	promslogConfig := &promslog.Config{
		Level:  level,
		Format: format,
	}
	logger := promslog.New(promslogConfig)
	return logger
}

func validateArgs(logger *slog.Logger) []error {
	var errs []error
	if *namespaceLabels == "" && *namespaceRegexp == "" {
		errs = append(errs, errors.New("Must provide either namespaces labels or namespace regexp"))
	}
	for _, err := range errs {
		logger.Error(err.Error())
	}
	return errs
}

func run(clientset kubernetes.Interface, logger *slog.Logger) error {
	namespaces, err := getNamespaces(clientset, logger)
	if err != nil {
		logger.Error("Error getting namespaces", "err", err)
		return err
	}
	activeNamespaces, err := getActiveNamespaces(logger)
	if err != nil {
		logger.Error("Error getting active namespaces", "err", err)
		return err
	}
	errCount := reap(namespaces, activeNamespaces, clientset, logger)
	if errCount > 0 {
		err := fmt.Errorf("%d errors encountered during reap", errCount)
		logger.Error(err.Error())
		return err
	}
	return nil
}

func getNamespaces(clientset kubernetes.Interface, logger *slog.Logger) ([]string, error) {
	var namespaces []string
	namespacePattern := regexp.MustCompile(*namespaceRegexp)
	nsLabels := strings.Split(*namespaceLabels, ",")
	if len(nsLabels) == 0 {
		nsLabels = []string{"all"}
	}
	for _, label := range nsLabels {
		nsListOptions := metav1.ListOptions{}
		if label != "all" {
			nsListOptions.LabelSelector = label
		}
		logger.Debug("Getting namespaces with label", "label", label)
		ns, err := clientset.CoreV1().Namespaces().List(context.TODO(), nsListOptions)
		if err != nil {
			logger.Error("Error getting namespace list", "label", label, "err", err)
			return nil, err
		}
		logger.Debug("Namespaces returned", "count", len(ns.Items))
		for _, namespace := range ns.Items {
			if *namespaceRegexp != "" && !namespacePattern.MatchString(namespace.Name) {
				logger.Debug("Skipping namespace that does not match namespace regexp", "namespace", namespace.Name)
				continue
			}
			currentAge := timeNow().Sub(namespace.CreationTimestamp.Time)
			if currentAge < *reapAfter {
				logger.Debug("Skipping namespace due to age", "namespace", namespace.Name, "age", currentAge.String())
				continue
			}
			if *namespaceLastUsedAnnotation != "" {
				if val, ok := namespace.Annotations[*namespaceLastUsedAnnotation]; ok {
					sec, err := strconv.ParseInt(val, 10, 64)
					if err != nil {
						logger.Error("Unable to parse namespace last used annotation", "namespace", namespace.Name, "err", err)
						continue
					}
					timeSinceLastUsed := timeNow().Sub(time.Unix(sec, 0))
					if timeSinceLastUsed < *lastUsedThreshold {
						logger.Debug("Skipping namespace due to recently used", "namespace", namespace.Name, "last-used", timeSinceLastUsed.String())
						continue
					}
				} else {
					logger.Debug("Namespace lacks last used annotation", "namespace", namespace.Name)
				}
			}
			namespaces = append(namespaces, namespace.Name)
		}
	}
	return namespaces, nil
}

func getActiveNamespaces(logger *slog.Logger) ([]string, error) {
	var namespaces []string
	client, err := api.NewClient(api.Config{
		Address: *prometheusAddress,
	})
	if err != nil {
		logger.Error("Error creating client", "err", err)
		return nil, err
	}

	v1api := v1.NewAPI(client)
	ctx, cancel := context.WithTimeout(context.Background(), *prometheusTimeout)
	defer cancel()
	var queryFilter string
	if *namespaceRegexp != "" {
		queryFilter = fmt.Sprintf("{namespace=~\"%s\"}", *namespaceRegexp)
	}
	query := fmt.Sprintf("max(max_over_time(timestamp(kube_pod_container_info%s)[%s:5m])) by (namespace)",
		queryFilter, (*reapAfter).String())
	result, warnings, err := v1api.Query(ctx, query, time.Now())
	if err != nil {
		logger.Error("Error querying Prometheus", "err", err)
		return nil, err
	}
	for _, warning := range warnings {
		logger.Warn("Warning querying Prometheus", "warning", warning)
	}
	if result.Type() == model.ValVector {
		vector := result.(model.Vector)
		for _, vec := range vector {
			if val, ok := vec.Metric["namespace"]; ok {
				namespaces = append(namespaces, string(val))
			}
		}
	} else {
		logger.Error("Unrecognized result type", "type", result.Type())
		return nil, err
	}
	return namespaces, nil
}

func reap(namespaces []string, activeNamespaces []string, clientset kubernetes.Interface, logger *slog.Logger) int {
	reaped := 0
	errCount := 0
	for _, namespace := range namespaces {
		namespaceLogger := logger.With("namespace", namespace)
		if sliceContains(activeNamespaces, namespace) {
			namespaceLogger.Debug("Skipping active namespace")
			continue
		}
		namespaceLogger.Info("Reaping namespace")
		err := clientset.CoreV1().Namespaces().Delete(context.TODO(), namespace, metav1.DeleteOptions{})
		if err != nil {
			errCount++
			namespaceLogger.Error("Error deleting namespace", "err", err)
			metricErrorsTotal.Inc()
		} else {
			reaped++
			metricReapedTotal.Inc()
		}
	}
	logger.Info("Reap summary", "namespaces", reaped)
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
