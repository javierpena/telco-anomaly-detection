package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

func TestRecoverPendingRuns(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "created on hub", false: "never created"}[present], func(t *testing.T) {
			ctx := context.Background()
			scheme := newTestScheme(t)
			record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
				Name: "run-a", Namespace: operatorNamespace,
				CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
				Labels:            map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key("cluster-a", "run-a")},
				Annotations:       map[string]string{healthcheckrun.PendingAnnotation: "true", healthcheckrun.HubOriginAnnotation: "true"},
			}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "cluster-a", AgenticRunName: "run-a",
				AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhasePending}}}
			hub := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(record).WithObjects(record).Build()
			if present {
				run := &unstructured.Unstructured{}
				run.SetAPIVersion(agenticrun.APIVersion)
				run.SetKind(agenticrun.Kind)
				run.SetName("run-a")
				run.SetNamespace(agenticrun.Namespace)
				run.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "telco-anomaly-detection"})
				_ = unstructured.SetNestedField(run.Object, "cluster-a", "spec", "targetCluster")
				if err := hub.Create(ctx, run); err != nil {
					t.Fatal(err)
				}
			}
			pending, err := recoverPendingRuns(ctx, hub, operatorNamespace)
			if err != nil || !pending {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			got := &ranv1alpha1.TelcoHealthCheckRun{}
			err = hub.Get(ctx, client.ObjectKeyFromObject(record), got)
			if present {
				if err != nil || got.Annotations[healthcheckrun.PendingAnnotation] != "" ||
					got.Status.AgenticRunStatus.Phase != healthcheckrun.PhaseCreated {
					t.Fatalf("record not confirmed: %+v, %v", got, err)
				}
			} else if err != nil || got.Annotations[healthcheckrun.PendingAnnotation] != "" ||
				got.Status.AgenticRunStatus.Phase != healthcheckrun.PhaseFailed {
				t.Fatalf("failed record not retained: %+v, %v", got, err)
			}
		})
	}
}

func TestRecoverPendingRunsLeavesLegacySnapshotUntouched(t *testing.T) {
	ctx := context.Background()
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "old-run", Namespace: operatorNamespace,
		CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
		Annotations:       map[string]string{healthcheckrun.PendingAnnotation: "true"},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke", AgenticRunName: "old-run",
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhasePending}}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(record).Build()
	if pending, err := recoverPendingRuns(ctx, hub, operatorNamespace); err != nil || pending {
		t.Fatalf("legacy record triggered recovery: pending=%t err=%v", pending, err)
	}
	var current ranv1alpha1.TelcoHealthCheckRun
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunStatus.Phase != healthcheckrun.PhasePending || current.Annotations[healthcheckrun.PendingAnnotation] != "true" {
		t.Fatalf("legacy record was modified: %+v", current)
	}
}

func TestRecoverPendingRunsRejectsMismatchedTarget(t *testing.T) {
	ctx := context.Background()
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "run-a", Namespace: operatorNamespace,
		CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
		Annotations:       map[string]string{healthcheckrun.PendingAnnotation: "true", healthcheckrun.HubOriginAnnotation: "true"},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke", AgenticRunName: "run-a",
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhasePending}}}
	run := hubRunObject(agenticrun.Kind)
	run.SetName("run-a")
	run.SetNamespace(agenticrun.Namespace)
	run.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "telco-anomaly-detection"})
	_ = unstructured.SetNestedField(run.Object, "wrong-spoke", "spec", "targetCluster")
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(record, run).Build()
	if pending, err := recoverPendingRuns(ctx, hub, operatorNamespace); err != nil || !pending {
		t.Fatalf("recovery pending=%t err=%v", pending, err)
	}
	var current ranv1alpha1.TelcoHealthCheckRun
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunStatus.Phase != healthcheckrun.PhasePending || current.Annotations[healthcheckrun.PendingAnnotation] != "true" {
		t.Fatalf("mismatched run was confirmed: %+v", current)
	}
}
