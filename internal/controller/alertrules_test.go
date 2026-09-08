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

func TestBuildCustomRulesYAML_BothDisabled(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: false, PodNetwork: false}
	yaml := buildCustomRulesYAML(alerts)
	if yaml != "groups: []\n" {
		t.Errorf("expected empty groups, got: %q", yaml)
	}
}

func TestBuildCustomRulesYAML_HostNetworkOnly(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: true, PodNetwork: false}
	yaml := buildCustomRulesYAML(alerts)
	if !strings.Contains(yaml, "telco-host-network") {
		t.Error("expected host-network group in YAML")
	}
	if strings.Contains(yaml, "telco-pod-network") {
		t.Error("unexpected pod-network group in YAML")
	}
}

func TestBuildCustomRulesYAML_PodNetworkOnly(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: false, PodNetwork: true}
	yaml := buildCustomRulesYAML(alerts)
	if strings.Contains(yaml, "telco-host-network") {
		t.Error("unexpected host-network group in YAML")
	}
	if !strings.Contains(yaml, "telco-pod-network") {
		t.Error("expected pod-network group in YAML")
	}
}

func TestBuildCustomRulesYAML_Both(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: true, PodNetwork: true}
	yaml := buildCustomRulesYAML(alerts)
	if !strings.Contains(yaml, "telco-host-network") {
		t.Error("expected host-network group in YAML")
	}
	if !strings.Contains(yaml, "telco-pod-network") {
		t.Error("expected pod-network group in YAML")
	}
}

func TestBuildCustomRulesYAML_HostNetworkAlertContent(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: true, PodNetwork: false}
	yaml := buildCustomRulesYAML(alerts)

	checks := []string{
		"TelcoHealthCheckHostNetwork",
		"instance:node_network_receive_drop_excluding_lo:rate1m",
		"instance:node_network_transmit_drop_excluding_lo:rate1m",
		"cluster:",
		"node:",
	}
	for _, s := range checks {
		if !strings.Contains(yaml, s) {
			t.Errorf("expected %q in host-network YAML", s)
		}
	}
}

func TestHostNetworkAlertNameParseable(t *testing.T) {
	yaml := "groups:\n" + hostNetworkRuleGroup()
	names, err := parseAlertNamesFromRulesYAML(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !names["TelcoHealthCheckHostNetwork"] {
		t.Error("expected TelcoHealthCheckHostNetwork to be parsed from host-network rule group")
	}
}

func TestReconcileAlertRules_Create(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	spec := ranv1alpha1.TelcoHealthcheckSpec{
		Alerts: ranv1alpha1.AlertsSpec{HostNetwork: true, PodNetwork: false},
	}
	if err := reconcileAlertRules(context.Background(), c, spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}

	if !strings.Contains(cm.Data[customRulesKey], "telco-host-network") {
		t.Error("expected host-network group in ConfigMap data")
	}
}

func TestReconcileAlertRules_Update(t *testing.T) {
	scheme := newTestScheme(t)
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      thanosRulerConfigMap,
			Namespace: observabilityNamespace,
		},
		Data: map[string]string{customRulesKey: "groups: []\n"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()

	spec := ranv1alpha1.TelcoHealthcheckSpec{
		Alerts: ranv1alpha1.AlertsSpec{HostNetwork: true, PodNetwork: true},
	}
	if err := reconcileAlertRules(context.Background(), c, spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, updated); err != nil {
		t.Fatalf("ConfigMap not found after update: %v", err)
	}
	if !strings.Contains(updated.Data[customRulesKey], "telco-host-network") {
		t.Error("expected host-network group after update")
	}
	if !strings.Contains(updated.Data[customRulesKey], "telco-pod-network") {
		t.Error("expected pod-network group after update")
	}
}

func TestParseAlertNamesFromRulesYAML(t *testing.T) {
	input := `groups:
  - name: test-group
    rules:
    - alert: MyAlertOne
      expr: up == 0
    - alert: MyAlertTwo
      expr: up == 0
`
	names, err := parseAlertNamesFromRulesYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !names["MyAlertOne"] {
		t.Error("expected MyAlertOne in parsed names")
	}
	if !names["MyAlertTwo"] {
		t.Error("expected MyAlertTwo in parsed names")
	}
}

func TestParseAlertNamesFromRulesYAML_Empty(t *testing.T) {
	names, err := parseAlertNamesFromRulesYAML("groups: []\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected 0 names, got %d", len(names))
	}
}

func TestBuildCustomRulesYAML_HostReservedCPUOnly(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: false, PodNetwork: false, HostReservedCPU: true}
	yaml := buildCustomRulesYAML(alerts)
	if !strings.Contains(yaml, "telco-host-reserved-cpu") {
		t.Error("expected host-reserved-cpu group in YAML")
	}
	if strings.Contains(yaml, "telco-host-network") {
		t.Error("unexpected host-network group in YAML")
	}
	if strings.Contains(yaml, "telco-pod-network") {
		t.Error("unexpected pod-network group in YAML")
	}
}

func TestBuildCustomRulesYAML_AllEnabled(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostNetwork: true, PodNetwork: true, HostReservedCPU: true}
	yaml := buildCustomRulesYAML(alerts)
	for _, group := range []string{"telco-host-network", "telco-pod-network", "telco-host-reserved-cpu"} {
		if !strings.Contains(yaml, group) {
			t.Errorf("expected %q in YAML", group)
		}
	}
}

func TestBuildCustomRulesYAML_HostReservedCPUAlertContent(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{HostReservedCPU: true}
	yaml := buildCustomRulesYAML(alerts)

	checks := []string{
		"TelcoHealthCheckHostReservedCPU",
		"openshift:cpu_usage_cores:sum",
		"cluster:",
	}
	for _, s := range checks {
		if !strings.Contains(yaml, s) {
			t.Errorf("expected %q in host-reserved-cpu YAML", s)
		}
	}
}

func TestHostReservedCPUAlertNameParseable(t *testing.T) {
	yaml := "groups:\n" + hostReservedCPURuleGroup()
	names, err := parseAlertNamesFromRulesYAML(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !names["TelcoHealthCheckHostReservedCPU"] {
		t.Error("expected TelcoHealthCheckHostReservedCPU to be parsed from host-reserved-cpu rule group")
	}
}

func TestCleanupAlertRules_DeletesConfigMap(t *testing.T) {
	scheme := newTestScheme(t)
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      thanosRulerConfigMap,
			Namespace: observabilityNamespace,
		},
		Data: map[string]string{customRulesKey: "groups: []\n"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()

	if err := cleanupAlertRules(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm)
	if err == nil {
		t.Error("expected ConfigMap to be deleted, but it still exists")
	}
}

func TestCleanupAlertRules_NotFound(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := cleanupAlertRules(context.Background(), c); err != nil {
		t.Errorf("expected nil when ConfigMap not found, got: %v", err)
	}
}

func TestBuildCustomRulesYAML_OVSProcessCPUOnly(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{OVSProcessCPU: true}
	yaml := buildCustomRulesYAML(alerts)
	if !strings.Contains(yaml, "telco-ovs-process-cpu") {
		t.Error("expected ovs-process-cpu group in YAML")
	}
	if strings.Contains(yaml, "telco-host-network") {
		t.Error("unexpected host-network group in YAML")
	}
	if strings.Contains(yaml, "telco-pod-network") {
		t.Error("unexpected pod-network group in YAML")
	}
	if strings.Contains(yaml, "telco-host-reserved-cpu") {
		t.Error("unexpected host-reserved-cpu group in YAML")
	}
}

func TestBuildCustomRulesYAML_OVSProcessCPUAlertContent(t *testing.T) {
	alerts := ranv1alpha1.AlertsSpec{OVSProcessCPU: true}
	yaml := buildCustomRulesYAML(alerts)

	checks := []string{
		"TelcoHealthCheckOVSProcessCPU",
		"ovs_db_process_cpu_seconds_total",
		"ovs_vswitchd_process_cpu_seconds_total",
		"cluster:",
		"node:",
	}
	for _, s := range checks {
		if !strings.Contains(yaml, s) {
			t.Errorf("expected %q in ovs-process-cpu YAML", s)
		}
	}
}

func TestOVSProcessCPUAlertNameParseable(t *testing.T) {
	yaml := "groups:\n" + ovsProcessCPURuleGroup()
	names, err := parseAlertNamesFromRulesYAML(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !names["TelcoHealthCheckOVSProcessCPU"] {
		t.Error("expected TelcoHealthCheckOVSProcessCPU to be parsed from ovs-process-cpu rule group")
	}
}
