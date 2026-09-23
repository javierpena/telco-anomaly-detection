package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildMetricsListYAML_Empty(t *testing.T) {
	yaml := buildMetricsListYAML(nil)
	if yaml != "names: []\n" {
		t.Errorf("expected empty names, got: %q", yaml)
	}
}

func TestBuildMetricsListYAML_EmptySlice(t *testing.T) {
	yaml := buildMetricsListYAML([]UserAlertConfig{})
	if yaml != "names: []\n" {
		t.Errorf("expected empty names for empty slice, got: %q", yaml)
	}
}

func TestBuildMetricsListYAML_SingleAlert(t *testing.T) {
	alerts := []UserAlertConfig{
		{AlertMetrics: []string{
			"container_network_receive_errors_total",
			"container_network_receive_packets_dropped_total",
		}},
	}
	yaml := buildMetricsListYAML(alerts)
	if !strings.Contains(yaml, "container_network_receive_errors_total") {
		t.Errorf("expected metric in output, got: %q", yaml)
	}
	if !strings.Contains(yaml, "container_network_receive_packets_dropped_total") {
		t.Errorf("expected metric in output, got: %q", yaml)
	}
	if !strings.HasPrefix(yaml, "names:\n") {
		t.Errorf("expected YAML to start with 'names:', got: %q", yaml)
	}
}

func TestBuildMetricsListYAML_NoMetricsInAlert(t *testing.T) {
	alerts := []UserAlertConfig{
		{AlertName: "SomeAlert", AlertRule: "- alert: SomeAlert\n  expr: up == 0"},
	}
	yaml := buildMetricsListYAML(alerts)
	if yaml != "names: []\n" {
		t.Errorf("expected empty names when alert has no metrics, got: %q", yaml)
	}
}

func TestBuildMetricsListYAML_Deduplication(t *testing.T) {
	alerts := []UserAlertConfig{
		{AlertMetrics: []string{"shared_metric", "metric_a"}},
		{AlertMetrics: []string{"shared_metric", "metric_b"}},
	}
	yaml := buildMetricsListYAML(alerts)
	count := strings.Count(yaml, "shared_metric")
	if count != 1 {
		t.Errorf("expected shared_metric to appear exactly once, got %d occurrences in: %q", count, yaml)
	}
	if !strings.Contains(yaml, "metric_a") {
		t.Error("expected metric_a in output")
	}
	if !strings.Contains(yaml, "metric_b") {
		t.Error("expected metric_b in output")
	}
}

func TestBuildMetricsListYAML_MultipleAlerts(t *testing.T) {
	alerts := []UserAlertConfig{
		{AlertMetrics: []string{"openshift:cpu_usage_cores:sum"}},
		{AlertMetrics: []string{"ovs_db_process_cpu_seconds_total", "ovs_vswitchd_process_cpu_seconds_total"}},
	}
	yaml := buildMetricsListYAML(alerts)
	for _, m := range []string{
		"openshift:cpu_usage_cores:sum",
		"ovs_db_process_cpu_seconds_total",
		"ovs_vswitchd_process_cpu_seconds_total",
	} {
		if !strings.Contains(yaml, m) {
			t.Errorf("expected metric %q in output, got: %q", m, yaml)
		}
	}
}

// makeMetricAlertCM returns a system-alert ConfigMap with alertMetrics for use in reconcile tests.
func makeMetricAlertCM(name, alertName, metricsJSON string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "test-ns",
			Labels: map[string]string{
				userAlertManagedByLabel: userAlertManagedByValue,
				systemAlertLabel:        systemAlertLabelValue,
			},
		},
		Data: map[string]string{
			"alertName":    alertName,
			"alertRule":    "- alert: " + alertName + "\n  expr: up == 0",
			"alertMetrics": metricsJSON,
		},
	}
}

func TestReconcileObservabilityMetrics_Creates(t *testing.T) {
	scheme := newTestScheme(t)
	sysCM := makeMetricAlertCM("telco-anomaly-pod-network-config", "TelcoHealthCheckPodNetwork",
		`["container_network_receive_errors_total","container_network_transmit_errors_total"]`)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sysCM).Build()

	if err := reconcileObservabilityMetrics(context.Background(), c, "test-ns", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      observabilityMetricsConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if cm.Labels["app.kubernetes.io/managed-by"] != "telco-anomaly-detection" {
		t.Errorf("unexpected managed-by label: %q", cm.Labels["app.kubernetes.io/managed-by"])
	}
	if !strings.Contains(cm.Data[metricsListKey], "container_network_receive_errors_total") {
		t.Errorf("expected metric in ConfigMap data, got: %q", cm.Data[metricsListKey])
	}
}

func TestReconcileObservabilityMetrics_Updates(t *testing.T) {
	scheme := newTestScheme(t)
	sysCM := makeMetricAlertCM("telco-anomaly-pod-network-config", "TelcoHealthCheckPodNetwork",
		`["container_network_receive_errors_total"]`)
	existingMetrics := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observabilityMetricsConfigMap,
			Namespace: observabilityNamespace,
		},
		Data: map[string]string{metricsListKey: "names: []\n"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sysCM, existingMetrics).Build()

	if err := reconcileObservabilityMetrics(context.Background(), c, "test-ns", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      observabilityMetricsConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found after update: %v", err)
	}
	if !strings.Contains(cm.Data[metricsListKey], "container_network_receive_errors_total") {
		t.Errorf("ConfigMap not updated; got: %q", cm.Data[metricsListKey])
	}
}

func TestReconcileObservabilityMetrics_NoSystemCMs_EmptyList(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := reconcileObservabilityMetrics(context.Background(), c, "test-ns", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      observabilityMetricsConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if cm.Data[metricsListKey] != "names: []\n" {
		t.Errorf("expected empty names list, got: %q", cm.Data[metricsListKey])
	}
}

func TestReconcileObservabilityMetrics_UserAlertsIncludedWhenEnabled(t *testing.T) {
	scheme := newTestScheme(t)
	userCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-user-alert",
			Namespace: "test-ns",
			Labels: map[string]string{
				userAlertManagedByLabel: userAlertManagedByValue,
				userAlertLabel:          userAlertLabelValue,
			},
		},
		Data: map[string]string{
			"alertName":    "MyCustomAlert",
			"alertRule":    "- alert: MyCustomAlert\n  expr: up == 0",
			"alertMetrics": `["my_custom_metric_total"]`,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(userCM).Build()

	if err := reconcileObservabilityMetrics(context.Background(), c, "test-ns", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      observabilityMetricsConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if !strings.Contains(cm.Data[metricsListKey], "my_custom_metric_total") {
		t.Errorf("expected user metric in output, got: %q", cm.Data[metricsListKey])
	}
}

func TestCleanupObservabilityMetrics_Deletes(t *testing.T) {
	scheme := newTestScheme(t)
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observabilityMetricsConfigMap,
			Namespace: observabilityNamespace,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	if err := cleanupObservabilityMetrics(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      observabilityMetricsConfigMap,
		Namespace: observabilityNamespace,
	}, cm)
	if err == nil {
		t.Error("expected ConfigMap to be deleted, but it still exists")
	}
}

func TestCleanupObservabilityMetrics_NotFound(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := cleanupObservabilityMetrics(context.Background(), c); err != nil {
		t.Fatalf("expected no error when ConfigMap not found, got: %v", err)
	}
}
