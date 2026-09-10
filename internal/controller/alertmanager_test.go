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

const baseAlertmanagerYAML = `global:
  resolve_timeout: 5m
route:
  receiver: default
  group_by:
  - cluster
receivers:
- name: default
`

func makeAlertManagerSecret(data string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      alertmanagerConfigSecret,
			Namespace: observabilityNamespace,
		},
		Data: map[string][]byte{
			alertmanagerConfigKey: []byte(data),
		},
	}
}

func TestUpsertWebhookReceiver_AddsReceiver(t *testing.T) {
	out, err := upsertWebhookReceiver([]byte(baseAlertmanagerYAML), alertReceiverName, "http://example.com/webhook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), alertReceiverName) {
		t.Errorf("expected receiver name %q in output, got:\n%s", alertReceiverName, string(out))
	}
	if !strings.Contains(string(out), "http://example.com/webhook") {
		t.Error("expected webhook URL in output")
	}
}

func TestUpsertWebhookReceiver_UpdatesExisting(t *testing.T) {
	initial := baseAlertmanagerYAML + `- name: ` + alertReceiverName + `
  webhook_configs:
  - url: http://old-url.com/webhook
`
	out, err := upsertWebhookReceiver([]byte(initial), alertReceiverName, "http://new-url.com/webhook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(out), "http://old-url.com/webhook") {
		t.Error("expected old webhook URL to be replaced")
	}
	if !strings.Contains(string(out), "http://new-url.com/webhook") {
		t.Error("expected new webhook URL in output")
	}
}

func TestUpsertWebhookReceiver_AddsRoute(t *testing.T) {
	out, err := upsertWebhookReceiver([]byte(baseAlertmanagerYAML), alertReceiverName, "http://example.com/webhook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outStr := string(out)
	if !strings.Contains(outStr, alertReceiverName) {
		t.Error("expected route with receiver name in output")
	}
	if !strings.Contains(outStr, "repeat_interval") {
		t.Error("expected repeat_interval in route to suppress re-notification while alert is firing")
	}
}

func TestUpsertWebhookReceiver_EmptyConfig(t *testing.T) {
	out, err := upsertWebhookReceiver([]byte(""), alertReceiverName, "http://example.com/webhook")
	if err != nil {
		t.Fatalf("unexpected error on empty config: %v", err)
	}
	if !strings.Contains(string(out), alertReceiverName) {
		t.Error("expected receiver in output even when starting from empty config")
	}
}

func TestReconcileAlertManagerReceiver(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeAlertManagerSecret(baseAlertmanagerYAML),
	).Build()

	url := "http://telco-anomaly-alert-receiver.telco-healthcheck-system.svc.cluster.local:8080/webhook"
	if err := reconcileAlertManagerReceiver(context.Background(), c, url); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      alertmanagerConfigSecret,
		Namespace: observabilityNamespace,
	}, updated); err != nil {
		t.Fatalf("failed to get updated secret: %v", err)
	}
	configData := string(updated.Data[alertmanagerConfigKey])
	if !strings.Contains(configData, alertReceiverName) {
		t.Error("expected webhook receiver in updated secret")
	}
}

func TestReconcileAlertManagerReceiver_MissingSecret(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	err := reconcileAlertManagerReceiver(context.Background(), c, "http://example.com/webhook")
	if err == nil {
		t.Error("expected error when secret does not exist, got nil")
	}
}

const alertmanagerYAMLWithReceiver = `global:
  resolve_timeout: 5m
route:
  receiver: default
  routes:
  - receiver: telco-anomaly-webhook
    continue: true
    repeat_interval: 24h
receivers:
- name: default
- name: telco-anomaly-webhook
  webhook_configs:
  - url: http://example.com/webhook
    send_resolved: true
`

func TestDeleteWebhookReceiver_RemovesReceiverAndRoute(t *testing.T) {
	out, err := deleteWebhookReceiver([]byte(alertmanagerYAMLWithReceiver), alertReceiverName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outStr := string(out)
	if strings.Contains(outStr, alertReceiverName) {
		t.Errorf("expected receiver %q to be removed, got:\n%s", alertReceiverName, outStr)
	}
	if strings.Contains(outStr, "http://example.com/webhook") {
		t.Error("expected webhook URL to be removed")
	}
}

func TestDeleteWebhookReceiver_ReceiverAbsent(t *testing.T) {
	out, err := deleteWebhookReceiver([]byte(baseAlertmanagerYAML), alertReceiverName)
	if err != nil {
		t.Fatalf("unexpected error when receiver is absent: %v", err)
	}
	if !strings.Contains(string(out), "default") {
		t.Error("expected other receivers to be preserved")
	}
}

func TestDeleteWebhookReceiver_EmptyConfig(t *testing.T) {
	out, err := deleteWebhookReceiver([]byte(""), alertReceiverName)
	if err != nil {
		t.Fatalf("unexpected error on empty config: %v", err)
	}
	if out == nil {
		t.Error("expected non-nil output for empty config")
	}
}

func TestRemoveAlertManagerReceiver_SecretNotFound(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := removeAlertManagerReceiver(context.Background(), c); err != nil {
		t.Errorf("expected nil when secret not found, got: %v", err)
	}
}

func TestRemoveAlertManagerReceiver_RemovesEntry(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeAlertManagerSecret(alertmanagerYAMLWithReceiver),
	).Build()

	if err := removeAlertManagerReceiver(context.Background(), c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      alertmanagerConfigSecret,
		Namespace: observabilityNamespace,
	}, updated); err != nil {
		t.Fatalf("failed to get secret: %v", err)
	}
	if strings.Contains(string(updated.Data[alertmanagerConfigKey]), alertReceiverName) {
		t.Errorf("expected receiver %q to be absent from secret after removal", alertReceiverName)
	}
}
