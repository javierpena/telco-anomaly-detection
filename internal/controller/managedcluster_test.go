package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		t.Fatalf("adding clusterv1 to scheme: %v", err)
	}
	if err := ranv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding ranv1alpha1 to scheme: %v", err)
	}
	return s
}

func makeManagedCluster(name string) *clusterv1.ManagedCluster {
	return &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func makeKubeconfigSecret(clusterName string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-admin-kubeconfig",
			Namespace: clusterName,
		},
		Data: map[string][]byte{
			"kubeconfig": []byte("kubeconfig-data-for-" + clusterName),
		},
	}
}

func TestGetMonitoredClusters_IncludeList(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeManagedCluster("cluster-a"),
		makeManagedCluster("cluster-b"),
		makeManagedCluster("cluster-c"),
	).Build()

	spec := ranv1alpha1.ManagedClustersSpec{
		Include: []string{"cluster-a", "cluster-c"},
	}
	clusters, err := getMonitoredClusters(context.Background(), c, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 2 {
		t.Errorf("expected 2 clusters, got %d: %v", len(clusters), clusters)
	}
	for _, name := range clusters {
		if name != "cluster-a" && name != "cluster-c" {
			t.Errorf("unexpected cluster in result: %q", name)
		}
	}
}

func TestGetMonitoredClusters_ExcludeList(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeManagedCluster("cluster-a"),
		makeManagedCluster("cluster-b"),
		makeManagedCluster("cluster-c"),
	).Build()

	spec := ranv1alpha1.ManagedClustersSpec{
		Exclude: []string{"cluster-b"},
	}
	clusters, err := getMonitoredClusters(context.Background(), c, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 2 {
		t.Errorf("expected 2 clusters, got %d: %v", len(clusters), clusters)
	}
	for _, name := range clusters {
		if name == "cluster-b" {
			t.Errorf("excluded cluster appeared in result: %q", name)
		}
	}
}

func TestGetMonitoredClusters_NoFilter(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeManagedCluster("cluster-a"),
		makeManagedCluster("cluster-b"),
	).Build()

	spec := ranv1alpha1.ManagedClustersSpec{}
	clusters, err := getMonitoredClusters(context.Background(), c, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 2 {
		t.Errorf("expected 2 clusters, got %d", len(clusters))
	}
}

func TestGetMonitoredClusters_IncludeNonExistent(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeManagedCluster("cluster-a"),
	).Build()

	spec := ranv1alpha1.ManagedClustersSpec{
		Include: []string{"does-not-exist"},
	}
	clusters, err := getMonitoredClusters(context.Background(), c, spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters, got %d: %v", len(clusters), clusters)
	}
}

func TestGetClusterKubeconfig(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		makeKubeconfigSecret("my-cluster"),
	).Build()

	data, err := getClusterKubeconfig(context.Background(), c, "my-cluster")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "kubeconfig-data-for-my-cluster" {
		t.Errorf("unexpected kubeconfig data: %q", string(data))
	}
}

func TestGetClusterKubeconfig_MissingSecret(t *testing.T) {
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := getClusterKubeconfig(context.Background(), c, "missing-cluster")
	if err == nil {
		t.Error("expected error for missing secret, got nil")
	}
}

func TestGetClusterKubeconfig_MissingKey(t *testing.T) {
	scheme := newTestScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-x-admin-kubeconfig",
			Namespace: "cluster-x",
		},
		Data: map[string][]byte{
			"wrong-key": []byte("data"),
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	_, err := getClusterKubeconfig(context.Background(), c, "cluster-x")
	if err == nil {
		t.Error("expected error for missing 'kubeconfig' key, got nil")
	}
}
