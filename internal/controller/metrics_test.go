package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func metricSamples(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}
	samples := make(map[string]float64)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			name := family.GetName()
			for _, label := range metric.GetLabel() {
				name += "{" + label.GetName() + "=" + label.GetValue() + "}"
			}
			samples[name] = metric.GetGauge().GetValue()
		}
	}
	return samples
}

func TestMetricsCollectorCurrentRecords(t *testing.T) {
	thc := &ranv1alpha1.TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName},
		Status:     ranv1alpha1.TelcoHealthcheckStatus{MonitoredClusters: []string{"one", "two"}},
	}
	makeRun := func(name, namespace, phase, action string) *ranv1alpha1.TelcoHealthCheckRun {
		run := &ranv1alpha1.TelcoHealthCheckRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Status:     ranv1alpha1.TelcoHealthCheckRunStatus{AgenticRunActionRequired: action},
		}
		if phase != "" {
			run.Status.AgenticRunStatus = &ranv1alpha1.AgenticRunStatus{Phase: phase}
		}
		return run
	}
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).
		WithStatusSubresource(thc, &ranv1alpha1.TelcoHealthCheckRun{}).
		WithObjects(thc,
			makeRun("pending", "operator", "Pending", ""),
			makeRun("pending-action", "operator", "Pending", "True"),
			makeRun("healthy", "operator", "NoActionRequired", "False"),
			makeRun("unknown", "operator", "", "True"),
			&ranv1alpha1.TelcoHealthCheckRun{
				ObjectMeta: metav1.ObjectMeta{Name: "empty-phase", Namespace: "operator"},
				Status: ranv1alpha1.TelcoHealthCheckRunStatus{
					AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{},
				},
			},
			makeRun("elsewhere", "other", "Failed", "True"),
		).Build()
	registry := prometheus.NewRegistry()
	if err := registry.Register(NewMetricsCollector(c, "operator")); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"telco_healthcheck_managed_clusters":                      2,
		"telco_healthcheck_runs":                                  5,
		"telco_healthcheck_runs_action_required":                  2,
		"telco_healthcheck_runs_by_phase{phase=Pending}":          2,
		"telco_healthcheck_runs_by_phase{phase=NoActionRequired}": 1,
		"telco_healthcheck_runs_by_phase{phase=Unknown}":          2,
	}
	if got := metricSamples(t, registry); !reflect.DeepEqual(got, want) {
		t.Errorf("initial metrics = %v, want %v", got, want)
	}

	if err := c.Delete(context.Background(), &ranv1alpha1.TelcoHealthCheckRun{
		ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: "operator"},
	}); err != nil {
		t.Fatal(err)
	}
	delete(want, "telco_healthcheck_runs_by_phase{phase=NoActionRequired}")
	want["telco_healthcheck_runs"] = 4
	if got := metricSamples(t, registry); !reflect.DeepEqual(got, want) {
		t.Errorf("metrics after deleting last record in phase = %v, want %v", got, want)
	}
}

func TestMetricsCollectorWithoutSingletonOrRuns(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	registry := prometheus.NewRegistry()
	if err := registry.Register(NewMetricsCollector(c, "operator")); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"telco_healthcheck_managed_clusters":     0,
		"telco_healthcheck_runs":                 0,
		"telco_healthcheck_runs_action_required": 0,
	}
	if got := metricSamples(t, registry); !reflect.DeepEqual(got, want) {
		t.Errorf("empty metrics = %v, want %v", got, want)
	}
}

type failingMetricsReader struct {
	client.Reader
	getErr  error
	listErr error
}

func (r failingMetricsReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if r.getErr != nil {
		return r.getErr
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func (r failingMetricsReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if r.listErr != nil {
		return r.listErr
	}
	return r.Reader.List(ctx, list, opts...)
}

func TestMetricsCollectorReadFailures(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	for _, tt := range []struct {
		name   string
		reader failingMetricsReader
	}{
		{name: "get", reader: failingMetricsReader{Reader: c, getErr: errors.New("get failed")}},
		{name: "list", reader: failingMetricsReader{Reader: c, listErr: errors.New("list failed")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			if err := registry.Register(NewMetricsCollector(tt.reader, "operator")); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Gather(); err == nil {
				t.Fatal("expected scrape to fail on API read error")
			}
		})
	}
}
