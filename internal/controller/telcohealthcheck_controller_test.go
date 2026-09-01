package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func makeTelcoHealthcheck(name, namespace string) *ranv1alpha1.TelcoHealthcheck {
	return &ranv1alpha1.TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: ranv1alpha1.TelcoHealthcheckSpec{
			ManagedClusters: ranv1alpha1.ManagedClustersSpec{
				Include: []string{"cluster-a"},
			},
			Alerts: ranv1alpha1.AlertsSpec{
				HostNetwork: true,
				PodNetwork:  false,
			},
			PeriodicHealthChecks: ranv1alpha1.PeriodicHealthChecksSpec{
				Period: metav1.Duration{Duration: 1 * time.Hour},
			},
		},
	}
}

func TestShouldRunCheck(t *testing.T) {
	t.Run("nil last run should trigger check", func(t *testing.T) {
		if !shouldRunCheck(nil, time.Minute) {
			t.Error("expected shouldRunCheck to return true when lastRun is nil")
		}
	})

	t.Run("expired period should trigger check", func(t *testing.T) {
		past := metav1.NewTime(time.Now().Add(-2 * time.Minute))
		if !shouldRunCheck(&past, time.Minute) {
			t.Error("expected shouldRunCheck to return true when period has elapsed")
		}
	})

	t.Run("recent run should not trigger check", func(t *testing.T) {
		recent := metav1.NewTime(time.Now().Add(-30 * time.Second))
		if shouldRunCheck(&recent, time.Minute) {
			t.Error("expected shouldRunCheck to return false when period has not elapsed")
		}
	})
}

func TestAlertReceiverURL_Default(t *testing.T) {
	r := &TelcoHealthcheckReconciler{
		OperatorNamespace: "telco-healthcheck-system",
	}
	url := r.alertReceiverURL("other-ns")
	expected := "http://telco-anomaly-alert-receiver.telco-healthcheck-system.svc.cluster.local:8080/webhook"
	if url != expected {
		t.Errorf("expected %q, got %q", expected, url)
	}
}

func TestAlertReceiverURL_Override(t *testing.T) {
	r := &TelcoHealthcheckReconciler{
		AlertReceiverSvcURL: "http://custom-url.example.com/webhook",
	}
	url := r.alertReceiverURL("any-ns")
	if url != "http://custom-url.example.com/webhook" {
		t.Errorf("expected override URL, got %q", url)
	}
}

func TestMapManagedClusterToTelcoHealthchecks(t *testing.T) {
	scheme := newTestScheme(t)
	thc1 := makeTelcoHealthcheck("thc-1", "ns-1")
	thc2 := makeTelcoHealthcheck("thc-2", "ns-2")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(thc1, thc2).Build()

	r := &TelcoHealthcheckReconciler{Client: c}
	requests := r.mapManagedClusterToTelcoHealthchecks(context.Background(), &clusterv1.ManagedCluster{})

	if len(requests) != 2 {
		t.Errorf("expected 2 reconcile requests, got %d", len(requests))
	}
}

func TestReconcile_NotFound(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &TelcoHealthcheckReconciler{
		Client:            c,
		Scheme:            scheme,
		OperatorNamespace: "telco-healthcheck-system",
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Errorf("expected nil error for not-found object, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found object")
	}
}

func TestReconcile_UpdatesMonitoredClusters(t *testing.T) {
	scheme := newTestScheme(t)

	thc := makeTelcoHealthcheck("test-thc", "default")
	cluster := makeManagedCluster("cluster-a")
	kubeconfigSecret := makeKubeconfigSecret("cluster-a")
	alertManagerSecret := makeAlertManagerSecret(baseAlertmanagerYAML)

	// Fake spoke client injected via buildSpokeClient override
	spokeFake := fake.NewClientBuilder().WithScheme(scheme).Build()
	origBuildSpokeClient := buildSpokeClient
	defer func() { buildSpokeClient = origBuildSpokeClient }()
	buildSpokeClient = func(_ []byte) (client.Client, error) {
		return spokeFake, nil
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		thc, cluster, kubeconfigSecret, alertManagerSecret,
	).WithStatusSubresource(thc).Build()

	r := &TelcoHealthcheckReconciler{
		Client:            c,
		Scheme:            scheme,
		OperatorNamespace: "telco-healthcheck-system",
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-thc", Namespace: "default"}}
	// First reconcile adds the finalizer and returns early.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected reconcile error (first pass): %v", err)
	}
	// Second reconcile performs the full reconcile with the finalizer in place.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("unexpected reconcile error (second pass): %v", err)
	}

	// Verify status was updated with monitored clusters
	updated := &ranv1alpha1.TelcoHealthcheck{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-thc", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get updated TelcoHealthcheck: %v", err)
	}
	if len(updated.Status.MonitoredClusters) != 1 || updated.Status.MonitoredClusters[0] != "cluster-a" {
		t.Errorf("unexpected monitored clusters: %v", updated.Status.MonitoredClusters)
	}
}

func TestReconcile_AddsFinalizer(t *testing.T) {
	scheme := newTestScheme(t)
	thc := makeTelcoHealthcheck("test-thc", "default")
	alertManagerSecret := makeAlertManagerSecret(baseAlertmanagerYAML)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(thc, alertManagerSecret).WithStatusSubresource(thc).Build()

	r := &TelcoHealthcheckReconciler{
		Client:            c,
		Scheme:            scheme,
		OperatorNamespace: "telco-healthcheck-system",
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-thc", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated := &ranv1alpha1.TelcoHealthcheck{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-thc", Namespace: "default"}, updated); err != nil {
		t.Fatalf("failed to get TelcoHealthcheck: %v", err)
	}
	found := false
	for _, f := range updated.Finalizers {
		if f == telcoHealthcheckFinalizer {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected finalizer %q to be set, got: %v", telcoHealthcheckFinalizer, updated.Finalizers)
	}
}

func TestReconcile_Deletion_CleansUpAndRemovesFinalizer(t *testing.T) {
	scheme := newTestScheme(t)
	now := metav1.Now()
	thc := makeTelcoHealthcheck("test-thc", "default")
	thc.Finalizers = []string{telcoHealthcheckFinalizer}
	thc.DeletionTimestamp = &now

	alertManagerSecret := makeAlertManagerSecret(baseAlertmanagerYAML)
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      thanosRulerConfigMap,
			Namespace: observabilityNamespace,
		},
		Data: map[string]string{customRulesKey: "groups: []\n"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(thc, alertManagerSecret, existingCM).Build()

	r := &TelcoHealthcheckReconciler{
		Client:            c,
		Scheme:            scheme,
		OperatorNamespace: "telco-healthcheck-system",
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-thc", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After removing the last finalizer, the fake client deletes the object (same as real server).
	// Verify either the object is gone or the finalizer was cleared.
	updated := &ranv1alpha1.TelcoHealthcheck{}
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "test-thc", Namespace: "default"}, updated)
	if getErr == nil {
		for _, f := range updated.Finalizers {
			if f == telcoHealthcheckFinalizer {
				t.Errorf("expected finalizer %q to be removed, but it still exists", telcoHealthcheckFinalizer)
			}
		}
	}
	// NotFound means the fake client removed the object after all finalizers were cleared — that is correct.

	// Verify ConfigMap was deleted.
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err == nil {
		t.Error("expected Thanos ConfigMap to be deleted")
	}
}

func TestReconcile_Deletion_NoResourcesStillRemovesFinalizer(t *testing.T) {
	scheme := newTestScheme(t)
	now := metav1.Now()
	thc := makeTelcoHealthcheck("test-thc", "default")
	thc.Finalizers = []string{telcoHealthcheckFinalizer}
	thc.DeletionTimestamp = &now

	// No Secret or ConfigMap in the fake client — cleanup returns nil (NotFound treated as success).
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(thc).Build()

	r := &TelcoHealthcheckReconciler{
		Client:            c,
		Scheme:            scheme,
		OperatorNamespace: "telco-healthcheck-system",
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-thc", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After removing the last finalizer, the fake client deletes the object (same as real server).
	// Verify either the object is gone or the finalizer was cleared.
	updated := &ranv1alpha1.TelcoHealthcheck{}
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "test-thc", Namespace: "default"}, updated)
	if getErr == nil {
		for _, f := range updated.Finalizers {
			if f == telcoHealthcheckFinalizer {
				t.Errorf("expected finalizer %q to be removed even when cleanup found no resources", telcoHealthcheckFinalizer)
			}
		}
	}
	// NotFound means the fake client removed the object after all finalizers were cleared — that is correct.
}
