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

func TestHubRunStatusCurrentAnalysisAndActionAlert(t *testing.T) {
	ctx := context.Background()
	run := hubRunObject(agenticrun.Kind)
	run.SetName("run-a")
	run.SetNamespace(agenticrun.Namespace)
	run.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "telco-anomaly-detection"})
	_ = unstructured.SetNestedField(run.Object, "spoke-a", "spec", "targetCluster")
	_ = unstructured.SetNestedSlice(run.Object, []interface{}{
		map[string]interface{}{"type": "Analyzed", "reason": "Succeeded", "lastTransitionTime": "2026-01-01T00:00:00Z"},
	}, "status", "conditions")
	_ = unstructured.SetNestedSlice(run.Object, []interface{}{
		map[string]interface{}{"name": "analysis-old"}, map[string]interface{}{"name": "analysis-new"},
	}, "status", "steps", "analysis", "results")
	old := hubRunObject("AnalysisResult")
	old.SetNamespace(agenticrun.Namespace)
	old.SetName("analysis-old")
	_ = unstructured.SetNestedField(old.Object, "run-a", "spec", "agenticRunName")
	_ = unstructured.SetNestedField(old.Object, "False", "status", "actionRequired")
	newResult := hubRunObject("AnalysisResult")
	newResult.SetNamespace(agenticrun.Namespace)
	newResult.SetName("analysis-new")
	_ = unstructured.SetNestedField(newResult.Object, "run-a", "spec", "agenticRunName")
	_ = unstructured.SetNestedField(newResult.Object, "True", "status", "actionRequired")
	_ = unstructured.SetNestedField(newResult.Object, "inspect pod", "status", "diagnosis", "summary")
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "run-a", Namespace: operatorNamespace,
		Annotations: map[string]string{healthcheckrun.HubOriginAnnotation: "true"},
		Labels:      map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key("spoke-a", "run-a")},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke-a", AgenticRunName: "run-a",
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhaseCreated}}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record, run, old, newResult).
		WithObjects(run, old, newResult, record).Build()
	r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace}
	if requests := r.mapHubResultToRecords(ctx, newResult); len(requests) != 1 || requests[0].Name != record.Name {
		t.Fatalf("result event did not map to record: %v", requests)
	}
	if err := r.syncHubRunStatus(ctx, record); err != nil {
		t.Fatal(err)
	}
	var current ranv1alpha1.TelcoHealthCheckRun
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunActionRequired != "True" || current.Status.AgenticRunStatus.Type != "Analyzed" ||
		current.Status.AgenticRunStatus.Phase != "Succeeded" || current.Status.AgenticRunStatus.Summary != "inspect pod" ||
		len(current.Status.AlertNotifications) != 1 {
		t.Fatalf("hub status: %+v", current.Status)
	}
	if err := r.syncHubRunStatus(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.AlertNotifications) != 1 {
		t.Fatal("replay enqueued duplicate notification")
	}
	if err := hub.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(run.Object, []interface{}{
		map[string]interface{}{"type": "Analyzed", "reason": "Succeeded", "lastTransitionTime": "2026-01-01T00:00:00Z"},
		map[string]interface{}{"type": "Executed", "reason": "Succeeded", "lastTransitionTime": "2026-01-01T00:01:00Z"},
	}, "status", "conditions")
	if err := hub.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := r.syncHubRunStatus(ctx, &current); err != nil {
		t.Fatal(err)
	}
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunActionRequired != "False" || len(current.Status.AlertNotifications) != 2 ||
		current.Status.AlertNotifications[1].Firing {
		t.Fatalf("resolution not queued: %+v", current.Status)
	}
	if err := hub.Delete(ctx, newResult); err != nil {
		t.Fatal(err)
	}
	if err := r.syncHubRunStatus(ctx, &current); err == nil {
		t.Fatal("missing referenced result should be retried, not treated as no action")
	}
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunActionRequired != "False" || len(current.Status.AlertNotifications) != 2 {
		t.Fatalf("result read error changed record: %+v", current.Status)
	}
	if err := hub.Delete(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := r.syncHubRunStatus(ctx, &current); err != nil {
		t.Fatal("expired hub run should leave historical status intact:", err)
	}
}

func TestHubRunStatusIgnoresLegacyAndMismatchedCluster(t *testing.T) {
	ctx := context.Background()
	run := hubRunObject(agenticrun.Kind)
	run.SetName("run-a")
	run.SetNamespace(agenticrun.Namespace)
	run.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "telco-anomaly-detection"})
	_ = unstructured.SetNestedField(run.Object, "other-spoke", "spec", "targetCluster")
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "run-a", Namespace: operatorNamespace,
		Labels: map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key("spoke-a", "run-a")},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke-a", AgenticRunName: "run-a"}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(record, run).Build()
	r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace}
	if err := r.syncHubRunStatus(ctx, record); err != nil {
		t.Fatal("legacy record was reconciled:", err)
	}
	record.Annotations = map[string]string{healthcheckrun.HubOriginAnnotation: "true"}
	if err := hub.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := r.syncHubRunStatus(ctx, record); err == nil {
		t.Fatal("mismatched target was accepted")
	}
	if got := r.mapHubRunToRecords(ctx, run); len(got) != 0 {
		t.Fatalf("mismatched target mapped to records: %v", got)
	}
}

func TestHubRunStatusReplaysAfterRecordArrivesFirst(t *testing.T) {
	ctx := context.Background()
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "run-late", Namespace: operatorNamespace,
		Annotations: map[string]string{healthcheckrun.HubOriginAnnotation: "true"},
		Labels:      map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key("spoke-a", "run-late")},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke-a", AgenticRunName: "run-late",
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhasePending}}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(record).Build()
	r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace}
	if err := r.syncHubRunStatus(ctx, record); err != nil {
		t.Fatal(err)
	}
	run := hubRunObject(agenticrun.Kind)
	run.SetName("run-late")
	run.SetNamespace(agenticrun.Namespace)
	run.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "telco-anomaly-detection"})
	_ = unstructured.SetNestedField(run.Object, "spoke-a", "spec", "targetCluster")
	_ = unstructured.SetNestedSlice(run.Object, []interface{}{
		map[string]interface{}{"type": "Analyzed", "reason": "Succeeded", "lastTransitionTime": "2026-01-01T00:00:00Z"},
	}, "status", "conditions")
	if err := hub.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if got := r.mapHubRunToRecords(ctx, run); len(got) != 1 || got[0].Name != record.Name {
		t.Fatalf("late run was not mapped to pending record: %v", got)
	}
	if err := r.syncHubRunStatus(ctx, record); err != nil {
		t.Fatal(err)
	}
	var current ranv1alpha1.TelcoHealthCheckRun
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunStatus.Type != "Analyzed" || current.Status.AgenticRunStatus.Phase != "Succeeded" {
		t.Fatalf("late run condition not replayed: %+v", current.Status)
	}
}

func TestLatestCondition(t *testing.T) {
	run := hubRunObject(agenticrun.Kind)
	stamp := time.Now().UTC().Format(time.RFC3339)
	_ = unstructured.SetNestedSlice(run.Object, []interface{}{
		map[string]interface{}{"type": "Analyzed", "reason": "Done", "lastTransitionTime": stamp},
		map[string]interface{}{"type": "Invalid", "reason": "Bad", "lastTransitionTime": "not-a-time"},
	}, "status", "conditions")
	if typ, phase := latestCondition(run); typ != "Analyzed" || phase != "Done" {
		t.Fatalf("latest condition = %q/%q", typ, phase)
	}
}
