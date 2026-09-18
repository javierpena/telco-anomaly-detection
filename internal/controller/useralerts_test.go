package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testNamespace = "telco-healthcheck-system"

func makeUserAlertCM(name, namespace string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				userAlertManagedByLabel: userAlertManagedByValue,
				userAlertLabel:          userAlertLabelValue,
			},
		},
		Data: data,
	}
}

func TestListUserAlertConfigs_ValidEntry(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("my-alert", testNamespace, map[string]string{
		"alertName":    "MyAlert",
		"alertRule":    "- alert: MyAlert\n  expr: up == 0",
		"alertMetrics": "my_metric_total\nanother_metric\n",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	got := configs[0]
	if got.AlertName != "MyAlert" {
		t.Errorf("AlertName: got %q, want %q", got.AlertName, "MyAlert")
	}
	if got.ConfigMapName != "my-alert" {
		t.Errorf("ConfigMapName: got %q, want %q", got.ConfigMapName, "my-alert")
	}
	if len(got.AlertMetrics) != 2 {
		t.Errorf("AlertMetrics: got %v, want 2 entries", got.AlertMetrics)
	}
}

func TestListUserAlertConfigs_MissingAlertNameSkipped(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("bad-alert", testNamespace, map[string]string{
		"alertRule": "- alert: SomeAlert\n  expr: up == 0",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs (missing alertName), got %d", len(configs))
	}
}

func TestListUserAlertConfigs_MissingAlertRuleSkipped(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("bad-rule", testNamespace, map[string]string{
		"alertName": "SomeAlert",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs (missing alertRule), got %d", len(configs))
	}
}

func TestListUserAlertConfigs_UnlabeledCMIgnored(t *testing.T) {
	scheme := newTestScheme(t)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "no-labels", Namespace: testNamespace},
		Data: map[string]string{
			"alertName": "SomeAlert",
			"alertRule": "- alert: SomeAlert\n  expr: up == 0",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs for unlabeled CM, got %d", len(configs))
	}
}

func TestListUserAlertConfigs_EmptyMetricsParsed(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("no-metrics", testNamespace, map[string]string{
		"alertName": "AlertNoMetrics",
		"alertRule": "- alert: AlertNoMetrics\n  expr: up == 0",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if len(configs[0].AlertMetrics) != 0 {
		t.Errorf("expected no metrics, got %v", configs[0].AlertMetrics)
	}
}
