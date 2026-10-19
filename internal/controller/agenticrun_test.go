package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

func makeAgenticRunConfigMap(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Data: map[string]string{
		"request": "test-request", "skills": `[{"image":"quay.io/test:latest","paths":["/skills/test.yaml"]}]`, "mcpServers": "[]",
	}}
}

type failingHubRunCreate struct {
	client.Client
	err error
}

func (c failingHubRunCreate) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetObjectKind().GroupVersionKind().Kind == agenticrun.Kind {
		return c.err
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestPeriodicHubRunCreation(t *testing.T) {
	ctx := context.Background()
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).
		WithObjects(makeAgenticRunConfigMap("telco-anomaly-rds-compliance-config", operatorNamespace)).Build()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: "telco-healthcheck", UID: "owner"}}
	clusters := []string{"cluster-a", "cluster-b"}
	if err := createAgenticRunsForClusters(ctx, hub, owner, clusters, "rds-compliance"); err != nil {
		t.Fatal(err)
	}
	runs := hubRunList(agenticrun.Kind)
	if err := hub.List(ctx, runs, client.InNamespace(agenticrun.Namespace)); err != nil || len(runs.Items) != 2 {
		t.Fatalf("hub runs: %v, %v", runs.Items, err)
	}
	var records ranv1alpha1.TelcoHealthCheckRunList
	if err := hub.List(ctx, &records, client.InNamespace(operatorNamespace)); err != nil || len(records.Items) != 2 {
		t.Fatalf("records: %v, %v", records.Items, err)
	}
	seen := map[string]bool{}
	for _, run := range runs.Items {
		cluster, _, _ := unstructured.NestedString(run.Object, "spec", "targetCluster")
		seen[cluster] = true
		if run.GetLabels()["telco-anomaly.io/healthcheck-ref"] != owner.Name {
			t.Errorf("missing owner label: %v", run.GetLabels())
		}
		var record ranv1alpha1.TelcoHealthCheckRun
		if err := hub.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: run.GetName()}, &record); err != nil {
			t.Fatal(err)
		}
		if record.Status.AgenticRunName != run.GetName() || record.Status.ClusterName != cluster ||
			record.Status.AgenticRunStatus.Phase != healthcheckrun.PhaseCreated ||
			record.Annotations[healthcheckrun.HubOriginAnnotation] != "true" || record.Annotations[healthcheckrun.PendingAnnotation] != "" {
			t.Errorf("incorrect record: %+v", record)
		}
	}
	if !seen[clusters[0]] || !seen[clusters[1]] || runs.Items[0].GetName() == runs.Items[1].GetName() {
		t.Fatalf("clusters lack distinct hub runs: %+v", runs.Items)
	}
}

func TestPeriodicHubCreateFailure(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		err         error
	}{
		{"definitive", healthcheckrun.PhaseFailed, apierrors.NewForbidden(schema.GroupResource{Group: agenticrun.Group, Resource: "agenticruns"}, "run", fmt.Errorf("denied"))},
		{"ambiguous", healthcheckrun.PhasePending, fmt.Errorf("connection lost")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).
				WithObjects(makeAgenticRunConfigMap("telco-anomaly-rds-compliance-config", operatorNamespace)).Build()
			c := failingHubRunCreate{Client: hub, err: tc.err}
			owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: "telco-healthcheck", UID: "owner"}}
			if err := createAgenticRunsForClusters(context.Background(), c, owner, []string{"cluster-a"}, "rds-compliance"); err != nil {
				t.Fatal(err)
			}
			var records ranv1alpha1.TelcoHealthCheckRunList
			if err := hub.List(context.Background(), &records, client.InNamespace(operatorNamespace)); err != nil || len(records.Items) != 1 || records.Items[0].Status.AgenticRunStatus.Phase != tc.phase {
				t.Fatalf("records: %+v, %v", records.Items, err)
			}
		})
	}
}

func TestPeriodicRunNameBounds(t *testing.T) {
	for _, tc := range []struct{ prefix, cluster string }{
		{"telco-health-low-latency-check-12345", "cluster-a"},
		{"telco-health-low-latency-check-12345", "cluster-b"},
		{"telco-health-low-latency-check-12345", strings.Repeat("a", 220) + ".example"},
		{strings.Repeat("a", 245), "cluster-a"},
	} {
		name := periodicRunName(tc.prefix, tc.cluster)
		if len(name) > 253 || name != periodicRunName(tc.prefix, tc.cluster) {
			t.Fatalf("invalid deterministic run name: %q", name)
		}
	}
	if periodicRunName("prefix", "cluster-a") == periodicRunName("prefix", "cluster-b") {
		t.Fatal("clusters collided")
	}
}

func TestPeriodicJitterCancellation(t *testing.T) {
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).Build()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: "telco-healthcheck", UID: "owner"}}
	ctx, cancel := context.WithCancel(context.Background())
	if err := createAgenticRunsForClustersWithConfig(ctx, hub, owner, []string{"cluster-a"}, "rds-compliance", &agenticrun.RunConfig{Request: "test"}, time.Hour, time.Hour, ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	var records ranv1alpha1.TelcoHealthCheckRunList
	if err := hub.List(context.Background(), &records); err != nil || len(records.Items) != 0 {
		t.Fatalf("canceled jitter created records: %v, %v", records.Items, err)
	}
}
