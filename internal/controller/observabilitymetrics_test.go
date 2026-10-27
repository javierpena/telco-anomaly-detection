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
	yaml, workloadYAML := buildMetricsListYAML(nil)
	if yaml != "names: []\n" {
		t.Errorf("expected empty names, got: %q", yaml)
	}
	if workloadYAML != "names: []\n" {
		t.Errorf("expected empty workload names, got: %q", workloadYAML)
	}
}

func TestBuildMetricsListYAML_EmptySlice(t *testing.T) {
	yaml, workloadYAML := buildMetricsListYAML([]UserAlertConfig{})
	if yaml != "names: []\n" {
		t.Errorf("expected empty names for empty slice, got: %q", yaml)
	}
	if workloadYAML != "names: []\n" {
		t.Errorf("expected empty workload names for empty slice, got: %q", workloadYAML)
	}
}

func TestBuildMetricsListYAML_SingleAlert(t *testing.T) {
	alerts := []UserAlertConfig{
		{AlertMetrics: []string{
			"container_network_receive_errors_total",
			"container_network_receive_packets_dropped_total",
		}},
	}
	yaml, workloadYAML := buildMetricsListYAML(alerts)
	if !strings.Contains(yaml, "container_network_receive_errors_total") {
		t.Errorf("expected metric in output, got: %q", yaml)
	}
	if !strings.Contains(yaml, "container_network_receive_packets_dropped_total") {
		t.Errorf("expected metric in output, got: %q", yaml)
	}
	if !strings.HasPrefix(yaml, "names:\n") {
		t.Errorf("expected YAML to start with 'names:', got: %q", yaml)
	}
	if workloadYAML != "names: []\n" {
		t.Errorf("expected platform metrics to be excluded from workload list, got: %q", workloadYAML)
	}
}

func TestBuildMetricsListYAML_NoMetricsInAlert(t *testing.T) {
	alerts := []UserAlertConfig{
		{AlertName: "SomeAlert", AlertRule: "- alert: SomeAlert\n  expr: up == 0"},
	}
	yaml, workloadYAML := buildMetricsListYAML(alerts)
	if yaml != "names: []\n" {
		t.Errorf("expected empty names when alert has no metrics, got: %q", yaml)
	}
	if workloadYAML != "names: []\n" {
		t.Errorf("expected empty workload names when alert has no metrics, got: %q", workloadYAML)
	}
}

func TestBuildMetricsListYAML_Deduplication(t *testing.T) {
	alerts := []UserAlertConfig{
		{AlertMetrics: []string{"shared_metric", "metric_a"}},
		{AlertMetrics: []string{"shared_metric", "metric_b"}},
	}
	yaml, _ := buildMetricsListYAML(alerts)
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
	yaml, _ := buildMetricsListYAML(alerts)
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

func TestBuildMetricsListYAML_SeparateDeduplicatedLists(t *testing.T) {
	alerts := []UserAlertConfig{
		{
			AlertMetrics:             []string{"platform_metric", "shared_metric", "platform_metric"},
			AlertUserWorkloadMetrics: []string{"app_requests_total", "shared_metric", "app_requests_total"},
		},
		{
			AlertMetrics:             []string{"shared_metric"},
			AlertUserWorkloadMetrics: []string{"app_requests_total", "app_errors_total"},
		},
	}
	metrics, workloadMetrics := buildMetricsListYAML(alerts)
	if want := "names:\n  - platform_metric\n  - shared_metric\n"; metrics != want {
		t.Errorf("platform list: got %q, want %q", metrics, want)
	}
	if want := "names:\n  - app_requests_total\n  - shared_metric\n  - app_errors_total\n"; workloadMetrics != want {
		t.Errorf("workload list: got %q, want %q", workloadMetrics, want)
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
	sysCM.Data["alertUserWorkloadMetrics"] = `["app_requests_total"]`
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
	if want := "names:\n  - app_requests_total\n"; cm.Data[userWorkloadMetricsListKey] != want {
		t.Errorf("workload list: got %q, want %q", cm.Data[userWorkloadMetricsListKey], want)
	}
	if strings.Contains(cm.Data[metricsListKey], "app_requests_total") {
		t.Error("workload metric leaked into platform list")
	}
}

func TestReconcileObservabilityMetrics_Updates(t *testing.T) {
	for _, tt := range []struct {
		name string
		data map[string]string
	}{
		{name: "upgrade single-key ConfigMap", data: map[string]string{metricsListKey: "names: []\n", "unrelated": "keep"}},
		{name: "replace stale lists", data: map[string]string{metricsListKey: "names:\n  - old_platform_metric\n", userWorkloadMetricsListKey: "names:\n  - old_app_metric\n", "unrelated": "keep"}},
		{name: "nil data"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sysCM := makeMetricAlertCM("system-alert", "SystemAlert", `["platform_metric"]`)
			sysCM.Data["alertUserWorkloadMetrics"] = `["app_requests_total"]`
			existingMetrics := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      observabilityMetricsConfigMap,
					Namespace: observabilityNamespace,
				},
				Data: tt.data,
			}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(sysCM, existingMetrics).Build()

			if err := reconcileObservabilityMetrics(context.Background(), c, "test-ns", false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			cm := &corev1.ConfigMap{}
			if err := c.Get(context.Background(), types.NamespacedName{
				Name: observabilityMetricsConfigMap, Namespace: observabilityNamespace,
			}, cm); err != nil {
				t.Fatalf("ConfigMap not found after update: %v", err)
			}
			if want := "names:\n  - platform_metric\n"; cm.Data[metricsListKey] != want {
				t.Errorf("platform list: got %q, want %q", cm.Data[metricsListKey], want)
			}
			if want := "names:\n  - app_requests_total\n"; cm.Data[userWorkloadMetricsListKey] != want {
				t.Errorf("workload list: got %q, want %q", cm.Data[userWorkloadMetricsListKey], want)
			}
			if tt.data != nil && cm.Data["unrelated"] != "keep" {
				t.Error("unrelated ConfigMap data was not preserved")
			}
		})
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
	if cm.Data[userWorkloadMetricsListKey] != "names: []\n" {
		t.Errorf("expected empty workload names list, got: %q", cm.Data[userWorkloadMetricsListKey])
	}
}

func TestReconcileObservabilityMetrics_UserAlerts(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			sysCM := makeMetricAlertCM("system-alert", "SystemAlert", `["system_platform_metric"]`)
			sysCM.Data["alertUserWorkloadMetrics"] = `["system_app_metric"]`
			userCM := makeUserAlertCM("user-alert", "test-ns", map[string]string{
				"alertName":                "UserAlert",
				"alertRule":                "- alert: UserAlert\n  expr: up == 0",
				"alertMetrics":             `["user_platform_metric"]`,
				"alertUserWorkloadMetrics": `["user_app_metric"]`,
			})
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(sysCM, userCM).Build()

			// First include user metrics, then verify disabling removes them as well.
			if err := reconcileObservabilityMetrics(context.Background(), c, "test-ns", true); err != nil {
				t.Fatalf("initial reconcile: %v", err)
			}
			if err := reconcileObservabilityMetrics(context.Background(), c, "test-ns", enabled); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			cm := &corev1.ConfigMap{}
			if err := c.Get(context.Background(), types.NamespacedName{
				Name: observabilityMetricsConfigMap, Namespace: observabilityNamespace,
			}, cm); err != nil {
				t.Fatalf("ConfigMap not found: %v", err)
			}
			wantPlatform := "names:\n  - system_platform_metric\n"
			wantWorkload := "names:\n  - system_app_metric\n"
			if enabled {
				wantPlatform += "  - user_platform_metric\n"
				wantWorkload += "  - user_app_metric\n"
			}
			if got := cm.Data[metricsListKey]; got != wantPlatform {
				t.Errorf("platform list: got %q, want %q", got, wantPlatform)
			}
			if got := cm.Data[userWorkloadMetricsListKey]; got != wantWorkload {
				t.Errorf("workload list: got %q, want %q", got, wantWorkload)
			}
		})
	}
}

func TestReconcileObservabilityMetrics_WorkloadMetricRemoval(t *testing.T) {
	ctx := context.Background()
	sysCM := makeMetricAlertCM("system-alert", "SystemAlert", "[]")
	sysCM.Data["alertUserWorkloadMetrics"] = `["app_requests_total", "app_errors_total"]`
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(sysCM).Build()
	for _, tt := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "initial workload-only alert", raw: `["app_requests_total", "app_errors_total"]`, want: "names:\n  - app_requests_total\n  - app_errors_total\n"},
		{name: "remove one metric", raw: `["app_requests_total"]`, want: "names:\n  - app_requests_total\n"},
		{name: "remove field", want: "names: []\n"},
		{name: "restore workload metric", raw: `["app_requests_total"]`, want: "names:\n  - app_requests_total\n"},
		{name: "delete alert", want: "names: []\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "delete alert" {
				if err := c.Delete(ctx, sysCM); err != nil {
					t.Fatalf("deleting alert: %v", err)
				}
			} else {
				delete(sysCM.Data, "alertUserWorkloadMetrics")
				if tt.raw != "" {
					sysCM.Data["alertUserWorkloadMetrics"] = tt.raw
				}
				if err := c.Update(ctx, sysCM); err != nil {
					t.Fatalf("updating alert: %v", err)
				}
			}
			if err := reconcileObservabilityMetrics(ctx, c, "test-ns", false); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			cm := &corev1.ConfigMap{}
			if err := c.Get(ctx, types.NamespacedName{
				Name: observabilityMetricsConfigMap, Namespace: observabilityNamespace,
			}, cm); err != nil {
				t.Fatalf("ConfigMap not found: %v", err)
			}
			if got := cm.Data[metricsListKey]; got != "names: []\n" {
				t.Errorf("expected empty platform list, got %q", got)
			}
			if got := cm.Data[userWorkloadMetricsListKey]; got != tt.want {
				t.Errorf("workload list: got %q, want %q", got, tt.want)
			}
		})
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
