package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func TestBuildMetricsListYAML_AllDisabled(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: false, PodNetwork: false}
	yaml := buildMetricsListYAML(alerts)
	if yaml != "names: []\n" {
		t.Errorf("expected empty names, got: %q", yaml)
	}
}

func TestBuildMetricsListYAML_PodNetworkEnabled(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{PodNetwork: true}
	yaml := buildMetricsListYAML(alerts)

	expectedMetrics := []string{
		"container_network_receive_errors_total",
		"container_network_receive_packets_dropped_total",
		"container_network_transmit_errors_total",
		"container_network_transmit_packets_dropped_total",
	}
	for _, m := range expectedMetrics {
		if !strings.Contains(yaml, m) {
			t.Errorf("expected metric %q in YAML, got: %q", m, yaml)
		}
	}
	if !strings.HasPrefix(yaml, "names:\n") {
		t.Errorf("expected YAML to start with 'names:', got: %q", yaml)
	}
}

func TestBuildMetricsListYAML_PodNetworkDisabled(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{PodNetwork: false}
	yaml := buildMetricsListYAML(alerts)
	if strings.Contains(yaml, "container_network") {
		t.Errorf("unexpected container_network metrics when PodNetwork disabled, got: %q", yaml)
	}
}

func TestReconcileObservabilityMetrics_Creates(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	alerts := ranv1alpha1.AlertsSpec{PodNetwork: true}

	if err := reconcileObservabilityMetrics(context.Background(), c, alerts); err != nil {
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
	content := cm.Data[metricsListKey]
	if !strings.Contains(content, "container_network_receive_errors_total") {
		t.Errorf("expected metric in ConfigMap data, got: %q", content)
	}
}

func TestReconcileObservabilityMetrics_Updates(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observabilityMetricsConfigMap,
			Namespace: observabilityNamespace,
		},
		Data: map[string]string{metricsListKey: "names: []\n"},
	}
	c := fake.NewClientBuilder().WithObjects(existing).Build()

	alerts := ranv1alpha1.AlertsSpec{PodNetwork: true}
	if err := reconcileObservabilityMetrics(context.Background(), c, alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      observabilityMetricsConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}

	if !strings.Contains(cm.Data[metricsListKey], "container_network_receive_errors_total") {
		t.Errorf("ConfigMap not updated; got: %q", cm.Data[metricsListKey])
	}
}

func TestReconcileObservabilityMetrics_PodNetworkDisabled(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	alerts := ranv1alpha1.AlertsSpec{PodNetwork: false}

	if err := reconcileObservabilityMetrics(context.Background(), c, alerts); err != nil {
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

func TestCleanupObservabilityMetrics_Deletes(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observabilityMetricsConfigMap,
			Namespace: observabilityNamespace,
		},
	}
	c := fake.NewClientBuilder().WithObjects(existing).Build()

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
	c := fake.NewClientBuilder().Build()

	if err := cleanupObservabilityMetrics(context.Background(), c); err != nil {
		t.Fatalf("expected no error when ConfigMap not found, got: %v", err)
	}
}

func TestBuildMetricsListYAML_OVSProcessCPUEnabled(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{OVSProcessCPU: true}
	yaml := buildMetricsListYAML(alerts)

	for _, m := range []string{
		"ovs_db_process_cpu_seconds_total",
		"ovs_vswitchd_process_cpu_seconds_total",
	} {
		if !strings.Contains(yaml, m) {
			t.Errorf("expected metric %q in YAML, got: %q", m, yaml)
		}
	}
	if !strings.HasPrefix(yaml, "names:\n") {
		t.Errorf("expected YAML to start with 'names:', got: %q", yaml)
	}
}

func TestBuildMetricsListYAML_OVSProcessCPUDisabled(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{OVSProcessCPU: false}
	yaml := buildMetricsListYAML(alerts)
	if strings.Contains(yaml, "ovs_") {
		t.Errorf("unexpected OVS metrics when OVSProcessCPU disabled, got: %q", yaml)
	}
}
