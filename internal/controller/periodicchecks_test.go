package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func TestReconcileSystemPeriodicConfigMaps_CreateWhenEnabled(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	periodic := ranv1alpha1.PeriodicHealthChecksSpec{
		RDSCompliance: ranv1alpha1.RDSComplianceSpec{Enabled: true},
	}
	if err := reconcileSystemPeriodicConfigMaps(context.Background(), c, "test-ns", periodic); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-rds-compliance-config",
		Namespace: "test-ns",
	}, cm); err != nil {
		t.Fatalf("expected rds-compliance CM to be created: %v", err)
	}
	if cm.Data["checkName"] != "rds-compliance" {
		t.Errorf("unexpected checkName: %q", cm.Data["checkName"])
	}
	if cm.Labels[systemPeriodicLabel] != systemPeriodicLabelValue {
		t.Errorf("expected system-periodic label, got: %q", cm.Labels[systemPeriodicLabel])
	}
}

func TestReconcileSystemPeriodicConfigMaps_NotCreatedWhenDisabled(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	periodic := ranv1alpha1.PeriodicHealthChecksSpec{
		RDSCompliance: ranv1alpha1.RDSComplianceSpec{Enabled: false},
	}
	if err := reconcileSystemPeriodicConfigMaps(context.Background(), c, "test-ns", periodic); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-rds-compliance-config",
		Namespace: "test-ns",
	}, cm)
	if err == nil {
		t.Error("expected rds-compliance CM to not exist when disabled, but it does")
	}
}

func TestReconcileSystemPeriodicConfigMaps_UpdatesExisting(t *testing.T) {
	scheme := newTestScheme(t)

	stale := &corev1.ConfigMap{}
	stale.Name = "telco-anomaly-rds-compliance-config"
	stale.Namespace = "test-ns"
	stale.Data = map[string]string{"request": "old prompt"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()

	periodic := ranv1alpha1.PeriodicHealthChecksSpec{
		RDSCompliance: ranv1alpha1.RDSComplianceSpec{Enabled: true},
	}
	if err := reconcileSystemPeriodicConfigMaps(context.Background(), c, "test-ns", periodic); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-rds-compliance-config",
		Namespace: "test-ns",
	}, updated); err != nil {
		t.Fatalf("CM not found: %v", err)
	}
	if updated.Data["checkName"] != "rds-compliance" {
		t.Errorf("expected updated checkName, got: %q", updated.Data["checkName"])
	}
}

func TestReconcileSystemPeriodicConfigMaps_DeletesWhenDisabled(t *testing.T) {
	scheme := newTestScheme(t)

	existing := &corev1.ConfigMap{}
	existing.Name = "telco-anomaly-rds-compliance-config"
	existing.Namespace = "test-ns"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	periodic := ranv1alpha1.PeriodicHealthChecksSpec{
		RDSCompliance: ranv1alpha1.RDSComplianceSpec{Enabled: false},
	}
	if err := reconcileSystemPeriodicConfigMaps(context.Background(), c, "test-ns", periodic); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-rds-compliance-config",
		Namespace: "test-ns",
	}, cm)
	if err == nil {
		t.Error("expected CM to be deleted when disabled, but it still exists")
	}
}

func TestCleanupSystemPeriodicConfigMaps(t *testing.T) {
	scheme := newTestScheme(t)

	existing := &corev1.ConfigMap{}
	existing.Name = "telco-anomaly-rds-compliance-config"
	existing.Namespace = "test-ns"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	if err := cleanupSystemPeriodicConfigMaps(context.Background(), c, "test-ns"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-rds-compliance-config",
		Namespace: "test-ns",
	}, cm)
	if err == nil {
		t.Error("expected CM to be deleted by cleanup, but it still exists")
	}
}

func TestCleanupSystemPeriodicConfigMaps_NoneExist(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := cleanupSystemPeriodicConfigMaps(context.Background(), c, "test-ns"); err != nil {
		t.Errorf("expected no error when CMs don't exist, got: %v", err)
	}
}
