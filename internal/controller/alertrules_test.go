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

// makeAlertCM returns a system-alert ConfigMap with the given fields for use in tests.
func makeAlertCM(name, alertName, groupName, alertRule string) *corev1.ConfigMap {
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
			"alertName":      alertName,
			"alertGroupName": groupName,
			"alertRule":      alertRule,
			"alertMetrics":   "[]",
		},
	}
}

func TestBuildCustomRulesYAML_Empty(t *testing.T) {
	yaml := buildCustomRulesYAML(nil)
	if yaml != "groups: []\n" {
		t.Errorf("expected empty groups, got: %q", yaml)
	}
}

func TestBuildCustomRulesYAML_EmptySlice(t *testing.T) {
	yaml := buildCustomRulesYAML([]UserAlertConfig{})
	if yaml != "groups: []\n" {
		t.Errorf("expected empty groups for empty slice, got: %q", yaml)
	}
}

func TestBuildCustomRulesYAML_SystemAlertWithGroupName(t *testing.T) {
	alerts := []UserAlertConfig{
		{
			AlertName: "TelcoHealthCheckHostNetwork",
			GroupName: "telco-host-network",
			AlertRule: "- alert: TelcoHealthCheckHostNetwork\n  expr: up == 0",
		},
	}
	yaml := buildCustomRulesYAML(alerts)
	if !strings.Contains(yaml, "telco-host-network") {
		t.Error("expected telco-host-network group name in YAML")
	}
	if !strings.Contains(yaml, "TelcoHealthCheckHostNetwork") {
		t.Error("expected alert name in YAML")
	}
}

func TestBuildCustomRulesYAML_UserAlertDefaultsGroupName(t *testing.T) {
	alerts := []UserAlertConfig{
		{
			AlertName: "MyCustomAlert",
			AlertRule: "- alert: MyCustomAlert\n  expr: up == 0",
		},
	}
	yaml := buildCustomRulesYAML(alerts)
	if !strings.Contains(yaml, "telco-user-mycustomalert") {
		t.Errorf("expected default group name telco-user-mycustomalert, got: %q", yaml)
	}
}

func TestBuildCustomRulesYAML_MixedSystemAndUser(t *testing.T) {
	alerts := []UserAlertConfig{
		{
			AlertName: "TelcoHealthCheckHostNetwork",
			GroupName: "telco-host-network",
			AlertRule: "- alert: TelcoHealthCheckHostNetwork\n  expr: up == 0",
		},
		{
			AlertName: "MyCustomAlert",
			AlertRule: "- alert: MyCustomAlert\n  expr: up == 0",
		},
	}
	yaml := buildCustomRulesYAML(alerts)
	if !strings.Contains(yaml, "telco-host-network") {
		t.Error("expected system group name")
	}
	if !strings.Contains(yaml, "telco-user-mycustomalert") {
		t.Error("expected user group name")
	}
}

func TestBuildCustomRulesYAML_AlertNameParseable(t *testing.T) {
	alerts := []UserAlertConfig{
		{
			AlertName: "TelcoHealthCheckHostNetwork",
			GroupName: "telco-host-network",
			AlertRule: "- alert: TelcoHealthCheckHostNetwork\n  expr: up == 0\n  for: 1m",
		},
	}
	yaml := buildCustomRulesYAML(alerts)
	names, err := parseAlertNamesFromRulesYAML(yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !names["TelcoHealthCheckHostNetwork"] {
		t.Error("expected TelcoHealthCheckHostNetwork to be parseable from built YAML")
	}
}

func TestReconcileAlertRules_Create(t *testing.T) {
	scheme := newTestScheme(t)
	sysCM := makeAlertCM("telco-anomaly-host-network-config",
		"TelcoHealthCheckHostNetwork", "telco-host-network",
		"- alert: TelcoHealthCheckHostNetwork\n  expr: up == 0")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sysCM).Build()

	if err := reconcileAlertRules(context.Background(), c, "test-ns", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("thanos ConfigMap not found: %v", err)
	}
	if !strings.Contains(cm.Data[customRulesKey], "telco-host-network") {
		t.Error("expected host-network group in ConfigMap data")
	}
}

func TestReconcileAlertRules_Update(t *testing.T) {
	scheme := newTestScheme(t)
	sysCM1 := makeAlertCM("telco-anomaly-host-network-config",
		"TelcoHealthCheckHostNetwork", "telco-host-network",
		"- alert: TelcoHealthCheckHostNetwork\n  expr: up == 0")
	sysCM2 := makeAlertCM("telco-anomaly-pod-network-config",
		"TelcoHealthCheckPodNetwork", "telco-pod-network",
		"- alert: TelcoHealthCheckPodNetwork\n  expr: up == 0")
	existingThanos := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      thanosRulerConfigMap,
			Namespace: observabilityNamespace,
		},
		Data: map[string]string{customRulesKey: "groups: []\n"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sysCM1, sysCM2, existingThanos).Build()

	if err := reconcileAlertRules(context.Background(), c, "test-ns", false); err != nil {
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

func TestReconcileAlertRules_NoSystemCMs_EmptyRules(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := reconcileAlertRules(context.Background(), c, "test-ns", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("thanos ConfigMap not found: %v", err)
	}
	if cm.Data[customRulesKey] != "groups: []\n" {
		t.Errorf("expected empty groups when no CMs, got: %q", cm.Data[customRulesKey])
	}
}

func TestReconcileAlertRules_UserAlertsIncludedWhenEnabled(t *testing.T) {
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
			"alertName": "MyCustomAlert",
			"alertRule": "- alert: MyCustomAlert\n  expr: up == 0",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(userCM).Build()

	if err := reconcileAlertRules(context.Background(), c, "test-ns", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		t.Fatalf("thanos ConfigMap not found: %v", err)
	}
	if !strings.Contains(cm.Data[customRulesKey], "telco-user-mycustomalert") {
		t.Errorf("expected user alert group in thanos CM, got: %q", cm.Data[customRulesKey])
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
