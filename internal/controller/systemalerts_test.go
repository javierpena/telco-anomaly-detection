package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func TestReconcileSystemAlertConfigMaps_CreateWhenEnabled(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	alerts := ranv1alpha1.AlertsSpec{HostNetwork: true}
	if err := reconcileSystemAlertConfigMaps(context.Background(), c, "test-ns", alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-host-network-config",
		Namespace: "test-ns",
	}, cm); err != nil {
		t.Fatalf("expected host-network CM to be created: %v", err)
	}
	if cm.Data["alertName"] != "TelcoHealthCheckHostNetwork" {
		t.Errorf("unexpected alertName: %q", cm.Data["alertName"])
	}
	if cm.Labels[systemAlertLabel] != systemAlertLabelValue {
		t.Errorf("expected system-alert label, got: %q", cm.Labels[systemAlertLabel])
	}
}

func TestReconcileSystemAlertConfigMaps_NotCreatedWhenDisabled(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	alerts := ranv1alpha1.AlertsSpec{HostNetwork: false}
	if err := reconcileSystemAlertConfigMaps(context.Background(), c, "test-ns", alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-host-network-config",
		Namespace: "test-ns",
	}, cm)
	if err == nil {
		t.Error("expected host-network CM to not exist when disabled, but it does")
	}
}

func TestReconcileSystemAlertConfigMaps_UpdatesExisting(t *testing.T) {
	scheme := newTestScheme(t)

	// Pre-create the CM with stale data.
	stale := &corev1.ConfigMap{}
	stale.Name = "telco-anomaly-host-network-config"
	stale.Namespace = "test-ns"
	stale.Data = map[string]string{"alertName": "OldName"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()

	alerts := ranv1alpha1.AlertsSpec{HostNetwork: true}
	if err := reconcileSystemAlertConfigMaps(context.Background(), c, "test-ns", alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-host-network-config",
		Namespace: "test-ns",
	}, updated); err != nil {
		t.Fatalf("CM not found: %v", err)
	}
	if updated.Data["alertName"] != "TelcoHealthCheckHostNetwork" {
		t.Errorf("expected updated alertName, got: %q", updated.Data["alertName"])
	}
}

func TestReconcileSystemAlertConfigMaps_DeletesWhenDisabled(t *testing.T) {
	scheme := newTestScheme(t)

	existing := &corev1.ConfigMap{}
	existing.Name = "telco-anomaly-host-network-config"
	existing.Namespace = "test-ns"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()

	alerts := ranv1alpha1.AlertsSpec{HostNetwork: false}
	if err := reconcileSystemAlertConfigMaps(context.Background(), c, "test-ns", alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cm := &corev1.ConfigMap{}
	err := c.Get(context.Background(), types.NamespacedName{
		Name:      "telco-anomaly-host-network-config",
		Namespace: "test-ns",
	}, cm)
	if err == nil {
		t.Error("expected CM to be deleted when disabled, but it still exists")
	}
}

func TestReconcileSystemAlertConfigMaps_AllAlerts(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	alerts := ranv1alpha1.AlertsSpec{
		HostNetwork:     true,
		PodNetwork:      true,
		HostReservedCPU: true,
		OVSProcessCPU:   true,
	}
	if err := reconcileSystemAlertConfigMaps(context.Background(), c, "test-ns", alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedCMs := []string{
		"telco-anomaly-host-network-config",
		"telco-anomaly-pod-network-config",
		"telco-anomaly-host-reserved-cpu-config",
		"telco-anomaly-ovs-process-cpu-config",
	}
	for _, name := range expectedCMs {
		cm := &corev1.ConfigMap{}
		if err := c.Get(context.Background(), types.NamespacedName{
			Name:      name,
			Namespace: "test-ns",
		}, cm); err != nil {
			t.Errorf("expected CM %q to be created: %v", name, err)
		}
	}
}

func TestCleanupSystemAlertConfigMaps(t *testing.T) {
	scheme := newTestScheme(t)

	existing := []*corev1.ConfigMap{
		{},
		{},
		{},
		{},
	}
	names := []string{
		"telco-anomaly-host-network-config",
		"telco-anomaly-pod-network-config",
		"telco-anomaly-host-reserved-cpu-config",
		"telco-anomaly-ovs-process-cpu-config",
	}
	var objs []interface{}
	for i, cm := range existing {
		cm.Name = names[i]
		cm.Namespace = "test-ns"
		objs = append(objs, cm)
	}

	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, cm := range existing {
		builder = builder.WithObjects(cm)
	}
	c := builder.Build()

	if err := cleanupSystemAlertConfigMaps(context.Background(), c, "test-ns"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range names {
		cm := &corev1.ConfigMap{}
		err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "test-ns"}, cm)
		if err == nil {
			t.Errorf("expected CM %q to be deleted, but it still exists", name)
		}
	}
}

func TestCleanupSystemAlertConfigMaps_NoneExist(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	if err := cleanupSystemAlertConfigMaps(context.Background(), c, "test-ns"); err != nil {
		t.Errorf("expected no error when CMs don't exist, got: %v", err)
	}
}
