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
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/promslog"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

var (
	creationTime, _ = time.Parse("01/02/2006 15:04:05", "01/01/2020 13:00:00")
)

func clientset() kubernetes.Interface {
	clientset := fake.NewSimpleClientset(&v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			CreationTimestamp: metav1.NewTime(creationTime),
		},
	}, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "user-user1",
			Labels: map[string]string{
				"app.kubernetes.io/name": "open-ondemand",
			},
			Annotations: map[string]string{
				// date --date="01/08/2020 14:00:00" +%s
				"openondemand.org/last-hook-execution": "1578510000",
			},
			CreationTimestamp: metav1.NewTime(creationTime),
		},
	}, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "user-user2",
			Labels: map[string]string{
				"app.kubernetes.io/name": "open-ondemand",
			},
			CreationTimestamp: metav1.NewTime(creationTime.Add(time.Hour * 24)),
		},
	}, &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "user-user3",
			Labels: map[string]string{
				"app.kubernetes.io/name": "foo",
			},
			Annotations: map[string]string{
				"openondemand.org/last-hook-execution": "foo",
			},
			CreationTimestamp: metav1.NewTime(creationTime.Add(time.Hour * 24)),
		},
	})
	return clientset
}

func TestGetNamespacesByLabel(t *testing.T) {
	if _, err := kingpin.CommandLine.Parse([]string{"--namespace-labels=app.kubernetes.io/name=open-ondemand", "--prometheus-address=foobar"}); err != nil {
		t.Fatal(err)
	}
	timeNow = func() time.Time {
		return creationTime.Add((time.Hour * 24 * 7) + time.Hour)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	clientset := clientset()
	namespaces, err := getNamespaces(clientset, logger)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(namespaces) != 1 {
		t.Errorf("Unexpected number of namespaces: %d", len(namespaces))
	}
	expected := []string{"user-user1"}
	sort.Strings(expected)
	sort.Strings(namespaces)
	if !reflect.DeepEqual(namespaces, expected) {
		t.Errorf("Unexpected value for namespaces\nExpected: %v\nGot: %v", expected, namespaces)
	}
}

func TestGetNamespacesByLabelLargerAge(t *testing.T) {
	if _, err := kingpin.CommandLine.Parse([]string{"--namespace-labels=app.kubernetes.io/name=open-ondemand", "--prometheus-address=foobar"}); err != nil {
		t.Fatal(err)
	}
	timeNow = func() time.Time {
		return creationTime.Add((time.Hour * 24 * 9))
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	clientset := clientset()
	namespaces, err := getNamespaces(clientset, logger)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(namespaces) != 2 {
		t.Errorf("Unexpected number of namespaces: %d", len(namespaces))
	}
	expected := []string{"user-user1", "user-user2"}
	sort.Strings(expected)
	sort.Strings(namespaces)
	if !reflect.DeepEqual(namespaces, expected) {
		t.Errorf("Unexpected value for namespaces\nExpected: %v\nGot: %v", expected, namespaces)
	}
}

func TestGetNamespacesByRegexp(t *testing.T) {
	if _, err := kingpin.CommandLine.Parse([]string{"--namespace-regexp=user-.+", "--prometheus-address=foobar"}); err != nil {
		t.Fatal(err)
	}
	timeNow = func() time.Time {
		return creationTime.Add((time.Hour * 24 * 9))
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	clientset := clientset()
	namespaces, err := getNamespaces(clientset, logger)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(namespaces) != 3 {
		t.Errorf("Unexpected number of namespaces: %d", len(namespaces))
	}
	expected := []string{"user-user1", "user-user2", "user-user3"}
	sort.Strings(expected)
	sort.Strings(namespaces)
	if !reflect.DeepEqual(namespaces, expected) {
		t.Errorf("Unexpected value for namespaces\nExpected: %v\nGot: %v", expected, namespaces)
	}
}

func TestGetNamespacesLastUsedAnnotation(t *testing.T) {
	args := []string{
		"--namespace-regexp=user-.+",
		"--namespace-last-used-annotation=openondemand.org/last-hook-execution",
		"--prometheus-address=foobar",
	}
	if _, err := kingpin.CommandLine.Parse(args); err != nil {
		t.Fatal(err)
	}
	timeNow = func() time.Time {
		return creationTime.Add((time.Hour * 24 * 7) + time.Hour)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	clientset := clientset()
	namespaces, err := getNamespaces(clientset, logger)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(namespaces) != 0 {
		t.Errorf("Unexpected number of namespaces: %d", len(namespaces))
	}
	timeNow = func() time.Time {
		return creationTime.Add((time.Hour * 24 * 8) + time.Hour)
	}
	namespaces, err = getNamespaces(clientset, logger)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(namespaces) != 2 {
		t.Errorf("Unexpected number of namespaces: %d", len(namespaces))
	}
	expected := []string{"user-user1", "user-user2"}
	sort.Strings(expected)
	sort.Strings(namespaces)
	if !reflect.DeepEqual(namespaces, expected) {
		t.Errorf("Unexpected value for namespaces\nExpected: %v\nGot: %v", expected, namespaces)
	}
}

func TestGetNamespacesByRegexpAndLabel(t *testing.T) {
	args := []string{
		"--prometheus-address=foobar",
		"--namespace-labels=app.kubernetes.io/name=open-ondemand",
		"--namespace-regexp=user-.+",
	}
	if _, err := kingpin.CommandLine.Parse(args); err != nil {
		t.Fatal(err)
	}
	timeNow = func() time.Time {
		return creationTime.Add((time.Hour * 24 * 9))
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	clientset := clientset()
	namespaces, err := getNamespaces(clientset, logger)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(namespaces) != 2 {
		t.Errorf("Unexpected number of namespaces: %d", len(namespaces))
	}
	expected := []string{"user-user1", "user-user2"}
	sort.Strings(expected)
	sort.Strings(namespaces)
	if !reflect.DeepEqual(namespaces, expected) {
		t.Errorf("Unexpected value for namespaces\nExpected: %v\nGot: %v", expected, namespaces)
	}
}

func TestGetActiveNamespaces(t *testing.T) {
	queryResults, err := os.ReadFile("testdata/prometheus-query.json")
	if err != nil {
		t.Fatalf("Error loading fixture data: %s", err.Error())
	}

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		_, _ = rw.Write(queryResults)
	}))
	defer server.Close()
	address, _ := url.Parse(server.URL)
	args := []string{fmt.Sprintf("--prometheus-address=%s", address)}
	if _, err := kingpin.CommandLine.Parse(args); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	activeNamespaces, err := getActiveNamespaces(logger)
	if err != nil {
		t.Errorf("Unexpected error %s", err.Error())
		return
	}
	if len(activeNamespaces) != 2 {
		t.Errorf("Unexpected number activeNamespaces, got %d", len(activeNamespaces))
		return
	}
	expectedActiveNamespaces := []string{"user-user1", "user-user3"}
	sort.Strings(activeNamespaces)
	sort.Strings(expectedActiveNamespaces)
	if !reflect.DeepEqual(activeNamespaces, expectedActiveNamespaces) {
		t.Errorf("Unexpected value for active namespaces\nExpected %v\nGot %v\n", expectedActiveNamespaces, activeNamespaces)
	}
}

func TestRun(t *testing.T) {
	queryResults, err := os.ReadFile("testdata/prometheus-query.json")
	if err != nil {
		t.Fatalf("Error loading fixture data: %s", err.Error())
	}

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		_, _ = rw.Write(queryResults)
	}))
	defer server.Close()
	address, _ := url.Parse(server.URL)
	args := []string{"--namespace-labels=app.kubernetes.io/name=open-ondemand", fmt.Sprintf("--prometheus-address=%s", address)}
	if _, err := kingpin.CommandLine.Parse(args); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	timeNow = func() time.Time {
		return creationTime.Add((time.Hour * 24 * 9))
	}

	clientset := clientset()
	err = run(clientset, logger)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	namespaces, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		t.Errorf("Unexpected error getting namespaces: %v", err)
	}
	if len(namespaces.Items) != 3 {
		t.Errorf("Unexpected number of namespaces, got: %d", len(namespaces.Items))
	}

	expected := `
	# HELP k8_namespace_reaper_error Indicates an error was encountered
	# TYPE k8_namespace_reaper_error gauge
	k8_namespace_reaper_error 0
	# HELP k8_namespace_reaper_errors_total Total number of errors
	# TYPE k8_namespace_reaper_errors_total counter
	k8_namespace_reaper_errors_total 0
	# HELP k8_namespace_reaper_reaped_total Total number of namespaces reaped
	# TYPE k8_namespace_reaper_reaped_total counter
	k8_namespace_reaper_reaped_total 1
	`

	if err := testutil.GatherAndCompare(metricGathers(), strings.NewReader(expected),
		"k8_namespace_reaper_reaped_total", "k8_namespace_reaper_error", "k8_namespace_reaper_errors_total"); err != nil {
		t.Errorf("unexpected collecting result:\n%s", err)
	}
}

func TestValidateArgs(t *testing.T) {
	if _, err := kingpin.CommandLine.Parse([]string{}); err == nil {
		t.Errorf("Expected error parsing lack of args")
	}
	if _, err := kingpin.CommandLine.Parse([]string{"--prometheus-address=foobar"}); err != nil {
		t.Errorf("Unexpected error parsing args")
	}
	err := validateArgs(promslog.NewNopLogger())
	if err == nil {
		t.Errorf("Expected error")
	}
}

func TestSetupLogging(t *testing.T) {
	baseArgs := []string{
		"--prometheus-address=foobar",
	}
	levels := []string{"debug", "info", "warn", "error"}
	for _, l := range levels {
		args := []string{fmt.Sprintf("--log-level=%s", l)}
		args = append(baseArgs, args...)
		if _, err := kingpin.CommandLine.Parse(args); err != nil {
			t.Fatal(err)
		}
		logger := setupLogging()
		if logger == nil {
			t.Errorf("Unexpected error getting logger")
		}
	}
	args := []string{"--log-format=json"}
	args = append(baseArgs, args...)
	if _, err := kingpin.CommandLine.Parse(args); err != nil {
		t.Fatal(err)
	}
	logger := setupLogging()
	if logger == nil {
		t.Errorf("Unexpected error getting logger")
	}
}
