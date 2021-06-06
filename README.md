[![CI Status](https://github.com/OSC/k8-namespace-reaper/workflows/test/badge.svg?branch=main)](https://github.com/OSC/k8-namespace-reaper/actions?query=workflow%3Atest)
[![GitHub release](https://img.shields.io/github/v/release/OSC/k8-namespace-reaper?include_prereleases&sort=semver)](https://github.com/OSC/k8-namespace-reaper/releases/latest)
![GitHub All Releases](https://img.shields.io/github/downloads/OSC/k8-namespace-reaper/total)
![Docker Pulls](https://img.shields.io/docker/pulls/ohiosupercomputer/k8-namespace-reaper)
[![Go Report Card](https://goreportcard.com/badge/github.com/OSC/k8-namespace-reaper?ts=1)](https://goreportcard.com/report/github.com/OSC/k8-namespace-reaper)
[![codecov](https://codecov.io/gh/OSC/k8-namespace-reaper/branch/main/graph/badge.svg)](https://codecov.io/gh/OSC/k8-namespace-reaper)

# k8-namespace-reaper

Kubernetes service that can reap namespaces that have not run pods recently.

This service is intended for namespaces such as user namespaces that might run job like pods but where the namespace doesn't necessarily need to always exist like if the user account was disabled.

Currently the namespaces to reap can be based on namespace regular expression and/or namespace labels . A namespace is reaped if the age of the namespace is past a certain threshold and no recent pods have run in that namespace. A Prometheus instance running [kube-state-metrics](https://github.com/kubernetes/kube-state-metrics) is required to check for recently run pods. See [Changing what is reaped](#changing-what-is-reaped) for details on how to configure reaping behavior.

Metrics about the count of reaped namespaces, duration of last reaping, and error counts can be queried using Prometheus `/metrics` endpoint exposed as a Service on port `8080`.

## Kubernetes support

Currently this code is built and tested against Kubernetes 1.21.

## Install

### Install with Helm

Only Helm 3 is supported.

```
helm repo add k8-namespace-reaper https://osc.github.io/k8-namespace-reaper
helm install k8-namespace-reaper k8-namespace-reaper/k8-namespace-reaper \
-n k8-namespace-reaper \
--prometheus-address=http://prometheus:9090
```

For Open OnDemand the following adjustments can be made to get a working install using Helm:

```
helm install k8-namespace-reaper k8-namespace-reaper/k8-namespace-reaper \
-n k8-namespace-reaper \
--prometheus-address=http://prometheus:9090
--set config.namespaceLabels='app.kubernetes.io/name=open-ondemand'
```

### Install with YAML

First install the necessary Namespace and RBAC resources:

```
kubectl apply -f https://github.com/OSC/k8-namespace-reaper/releases/latest/download/namespace-rbac.yaml
```

For Open OnDemand a deployment can be installed using Open OnDemand specific deployment:

```
kubectl apply -f https://github.com/OSC/k8-namespace-reaper/releases/latest/download/ondemand-deployment.yaml
```

A more generic deployment:

```
kubectl apply -f https://github.com/OSC/k8-namespace-reaper/releases/latest/download/deployment.yaml
```

**NOTE** Both the OnDemand and generic deployments require modifications to set Prometheus address. The generic deployment also needs to be told which namespaces to reap.

### Changing what is reaped

If you wish to scope the namespaces searched for reaping change either `--namespace-labels` flag (comma separated) to limit namespaces searched by label, or a namespace regular expression with `--namespace-regexp`. The namespace regular expression is also used to limit the scope of the Prometheus query, so that regular expression must also be valid for PromQL.

The minimum age of a namespace to reap is set with `--reap-after`. This flag also sets how far back to look for active namespaces by looking at pod metrics. If `--reap-after` is default of `168h` then a namespace older than 7 days with no pods active in last 7 days will be deleted.

## Configuration Details

The k8-namespace-reaper is intended to be deployed inside a Kubernetes cluster. It can also be run outside the cluster via cron.

The following flags and environment variables can modify the behavior of the k8-namespace-reaper:

| Flag    | Environment Variable | Description |
|---------|----------------------|-------------|
| --namespace-labels | NAMESPACE_LABELS | Sets namespaces labels for which namespaces to consider for reaping, required if `--namespace-regexp` is not set. |
| --namespace-regexp | NAMESPACE_REGEXP | Sets namespace regular expression for which namespaces to consider for reaping, required if `--namespace-labels` is not set. |
| --prometheus-address | PROMETHEUS_ADDRESS | Prometheus address, eg: http://prometheus:9090, this is required |
| --prometheus-timeout=30s | PROMETHEUS_TIMEOUT=30s | Prometheus query timeout [Duration](https://golang.org/pkg/time/#ParseDuration) |
| --reap-after=168h | REAP_AFTER=168h |  [Duration](https://golang.org/pkg/time/#ParseDuration) minimum age of namespaces to reap as well as how far back to look for active pods |
| --interval=6h | INTERVAL=6h | [Duration](https://golang.org/pkg/time/#ParseDuration) between each reaping execution when run in loop |
| --listen-address=:8080 | LISTEN_ADDRESS=:8080| Address to listen for HTTP requests |
| --no-process-metrics | PROCESS_METRICS=false | Disable metrics about the running processes such as CPU, memory and Go stats |
| --run-once | RUN_ONCE=true | Set to only execute reap code once and exit, ie used when run via cron|
| --kubeconfig | KUBECONFIG | The path to Kubernetes config, required when run outside Kubernetes |
| --log-level=info | LOG_LEVEL=info | The logging level One of: [debug, info, warn, error] |
| --log-format=logfmt | LOG_FORMAT=logfmt | The logging format, either logfmt or json |
