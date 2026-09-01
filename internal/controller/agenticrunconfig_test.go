package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureAgenticRunConfigs_CreatesAll(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := ensureAgenticRunConfigs(context.Background(), c, operatorNamespace); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range agenticRunConfigMapNames {
		cm := &corev1.ConfigMap{}
		if err := c.Get(context.Background(), client.ObjectKey{Name: name, Namespace: operatorNamespace}, cm); err != nil {
			t.Errorf("expected ConfigMap %s to exist, got error: %v", name, err)
			continue
		}
		if cm.Data["request"] == "" {
			t.Errorf("ConfigMap %s missing 'request' key", name)
		}
	}
}

func TestEnsureAgenticRunConfigs_LeavesExistingUnchanged(t *testing.T) {
	scheme := newTestScheme(t)

	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "telco-anomaly-rds-compliance-config",
			Namespace: operatorNamespace,
		},
		Data: map[string]string{
			"request": "custom-request",
			"skills":  `[{"image":"quay.io/custom:v1","paths":["/custom/skill.yaml"]}]`,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	if err := ensureAgenticRunConfigs(context.Background(), c, operatorNamespace); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{
		Name:      "telco-anomaly-rds-compliance-config",
		Namespace: operatorNamespace,
	}, got); err != nil {
		t.Fatalf("getting ConfigMap: %v", err)
	}
	if got.Data["request"] != "custom-request" {
		t.Errorf("expected preserved custom request, got %q", got.Data["request"])
	}
}
