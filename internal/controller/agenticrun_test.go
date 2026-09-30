package controller

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

// makeAgenticRunConfigMap builds a ConfigMap suitable for use as an AgenticRun config in tests.
func makeAgenticRunConfigMap(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data: map[string]string{
			"request":    "test-request",
			"skills":     `[{"image":"quay.io/test:latest","paths":["/skills/test.yaml"]}]`,
			"mcpServers": "[]",
		},
	}
}

func TestCreateAgenticRunsForClusters(t *testing.T) {
	scheme := newTestScheme(t)

	// Spoke cluster uses an empty scheme – AgenticRun is created as unstructured.
	spokeScheme := runtime.NewScheme()
	spokeA := fake.NewClientBuilder().WithScheme(spokeScheme).Build()
	spokeB := fake.NewClientBuilder().WithScheme(spokeScheme).Build()

	original := buildSpokeClient
	defer func() { buildSpokeClient = original }()
	buildSpokeClient = func(kubeconfig []byte) (client.Client, error) {
		if string(kubeconfig) == "kubeconfig-data-for-cluster-a" {
			return spokeA, nil
		}
		return spokeB, nil
	}

	hubClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).WithObjects(
		makeKubeconfigSecret("cluster-a"),
		makeKubeconfigSecret("cluster-b"),
		makeAgenticRunConfigMap("telco-anomaly-rds-compliance-config", operatorNamespace),
	).Build()

	thc := &ranv1alpha1.TelcoHealthcheck{}
	thc.Name = "test-healthcheck"
	thc.UID = "test-owner-uid"
	thc.Namespace = "default"

	if err := createAgenticRunsForClusters(context.Background(), hubClient, thc, []string{"cluster-a", "cluster-b"}, "rds-compliance"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify AgenticRun objects were created with the expected controller label.
	runList := &unstructured.UnstructuredList{}
	runList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   agenticrun.Group,
		Version: agenticrun.Version,
		Kind:    agenticrun.Kind + "List",
	})
	if err := spokeA.List(context.Background(), runList); err != nil {
		t.Fatalf("listing AgenticRuns: %v", err)
	}
	if len(runList.Items) == 0 {
		t.Error("expected at least one AgenticRun to be created")
	}
	for _, item := range runList.Items {
		if item.GetLabels()["telco-anomaly.io/healthcheck-ref"] != "test-healthcheck" {
			t.Errorf("expected healthcheck-ref label 'test-healthcheck', got %q", item.GetLabels()["telco-anomaly.io/healthcheck-ref"])
		}
	}
	var records ranv1alpha1.TelcoHealthCheckRunList
	if err := hubClient.List(context.Background(), &records, client.InNamespace(operatorNamespace)); err != nil {
		t.Fatal(err)
	}
	if len(records.Items) != 2 {
		t.Fatalf("expected one record per cluster, got %d", len(records.Items))
	}
	for _, record := range records.Items {
		if record.Name != record.Status.AgenticRunName+"-"+record.Status.ClusterName ||
			record.Status.TriggeredBy != ranv1alpha1.TriggerTypePeriodicHealthCheck ||
			record.Status.Trigger != "rds-compliance" || record.Annotations[healthcheckrun.PendingAnnotation] != "" ||
			len(record.OwnerReferences) != 1 || record.OwnerReferences[0].UID != thc.UID {
			t.Errorf("incorrect periodic record: %+v", record)
		}
	}
}

func TestCreateAgenticRunsForClusters_SkipsBadSpokeClient(t *testing.T) {
	scheme := newTestScheme(t)

	original := buildSpokeClient
	defer func() { buildSpokeClient = original }()
	buildSpokeClient = func(_ []byte) (client.Client, error) {
		return nil, fmt.Errorf("cannot connect to cluster")
	}

	hubClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).WithObjects(
		makeKubeconfigSecret("cluster-a"),
		makeAgenticRunConfigMap("telco-anomaly-rds-compliance-config", operatorNamespace),
	).Build()

	thc := &ranv1alpha1.TelcoHealthcheck{}
	thc.Name = "test"
	thc.UID = "test-owner-uid"
	thc.Namespace = "default"

	// Should return nil: errors per-cluster are logged and skipped, not propagated.
	if err := createAgenticRunsForClusters(context.Background(), hubClient, thc, []string{"cluster-a"}, "rds-compliance"); err != nil {
		t.Errorf("expected nil error (cluster skipped), got: %v", err)
	}
}

func TestCreateAgenticRunsForClusters_SkipsMissingKubeconfig(t *testing.T) {
	scheme := newTestScheme(t)

	// No secrets in the hub – getClusterKubeconfig will fail.
	hubClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).WithObjects(
		makeAgenticRunConfigMap("telco-anomaly-rds-compliance-config", operatorNamespace),
	).Build()

	thc := &ranv1alpha1.TelcoHealthcheck{}
	thc.Name = "test"
	thc.UID = "test-owner-uid"
	thc.Namespace = "default"

	if err := createAgenticRunsForClusters(context.Background(), hubClient, thc, []string{"no-secret-cluster"}, "rds-compliance"); err != nil {
		t.Errorf("expected nil error (kubeconfig fetch failed, cluster skipped), got: %v", err)
	}
}

func TestCreateAgenticRunsForClusters_UnknownCheckType(t *testing.T) {
	scheme := newTestScheme(t)
	hubClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	thc := &ranv1alpha1.TelcoHealthcheck{}
	thc.Name = "test"
	thc.Namespace = "default"

	err := createAgenticRunsForClusters(context.Background(), hubClient, thc, []string{"cluster-a"}, "unknown-type")
	if err == nil {
		t.Error("expected error for unmapped check type, got nil")
	}
}
