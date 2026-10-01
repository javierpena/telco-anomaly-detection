package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

func TestLatestCondition(t *testing.T) {
	for _, tt := range []struct {
		name                string
		conditions          []interface{}
		wantType, wantPhase string
	}{
		{"latest timestamp, not list order", []interface{}{
			map[string]interface{}{"lastTransitionTime": "2026-01-02T11:00:00Z", "type": "Complete", "reason": "Succeeded"},
			map[string]interface{}{"lastTransitionTime": "2026-01-02T10:00:00Z", "type": "Progressing", "reason": "Running"},
		}, "Complete", "Succeeded"},
		{"invalid timestamps", []interface{}{
			map[string]interface{}{"lastTransitionTime": "invalid", "type": "Failed", "reason": "Failure"},
			map[string]interface{}{"lastTransitionTime": "2026-01-02T11:00:00.123Z", "type": "Complete", "reason": "Completed"},
			map[string]interface{}{"type": "Pending", "reason": "Waiting"},
		}, "Complete", "Completed"},
		{"no timestamp", []interface{}{map[string]interface{}{"type": "Pending", "reason": "Waiting"}}, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run := &unstructured.Unstructured{Object: map[string]interface{}{
				"status": map[string]interface{}{"conditions": tt.conditions},
			}}
			gotType, gotPhase := latestCondition(run)
			if gotType != tt.wantType || gotPhase != tt.wantPhase {
				t.Fatalf("latestCondition() = (%q, %q), want (%q, %q)", gotType, gotPhase, tt.wantType, tt.wantPhase)
			}
		})
	}
}

func TestExtractResultSummary(t *testing.T) {
	result := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"diagnosis": map[string]interface{}{"summary": "no issue found"},
		},
	}}
	if got := extractResultSummary(result); got != "no issue found" {
		t.Fatalf("unexpected summary: %q", got)
	}
	result.Object["status"] = map[string]interface{}{"options": []interface{}{map[string]interface{}{"diagnosis": map[string]interface{}{"summary": "fix needed"}}}}
	if got := extractResultSummary(result); got != "fix needed" {
		t.Fatalf("missing option diagnosis: %q", got)
	}
}

func TestHandleResultMatchesClusterAndRun(t *testing.T) {
	ctx := context.Background()
	const cluster, run = "cluster-a", "run-a"
	makeRecord := func(name, clusterName string) *ranv1alpha1.TelcoHealthCheckRun {
		return &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: operatorNamespace, Labels: map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key(clusterName, run)},
		}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: clusterName, AgenticRunName: run,
			AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Type: "Analyzed", Phase: healthcheckrun.PhaseCreated}}}
	}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).
		WithObjects(makeRecord("match", cluster), makeRecord("other", "cluster-b")).Build()
	m := newSpokeWatchManager(hub, hub, operatorNamespace)
	dyn := newResultClient()
	result := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"agenticRunName": run},
		"status": map[string]interface{}{"actionRequired": "True", "conditions": []interface{}{
			map[string]interface{}{"lastTransitionTime": "2026-01-02T10:00:00Z", "reason": "Completed"},
		}, "diagnosis": map[string]interface{}{"summary": "healthy"}},
	}}
	m.handleResult(ctx, cluster, result)
	for _, tt := range []struct {
		name string
		want bool
	}{{"match", true}, {"other", false}} {
		record := &ranv1alpha1.TelcoHealthCheckRun{}
		if err := hub.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: tt.name}, record); err != nil {
			t.Fatal(err)
		}
		if tt.want && (record.Status.AgenticRunStatus.Phase != healthcheckrun.PhaseCreated ||
			record.Status.AgenticRunStatus.Summary != "healthy" || record.Status.AgenticRunActionRequired != "True") {
			t.Errorf("record %s: unexpected result %+v", tt.name, record.Status)
		}
		if !tt.want && (record.Status.AgenticRunStatus.Phase != healthcheckrun.PhaseCreated || record.Status.AgenticRunActionRequired != "") {
			t.Errorf("record %s: unexpectedly updated %+v", tt.name, record.Status)
		}
	}
	status := result.Object["status"].(map[string]interface{})
	status["actionRequired"] = "False"
	status["conditions"] = []interface{}{map[string]interface{}{
		"lastTransitionTime": "2026-01-02T11:00:00Z", "reason": "NoActionRequired",
	}}
	m.handleResult(ctx, cluster, result)
	runObject := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": run},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"lastTransitionTime": "2026-01-02T11:00:00Z", "type": "Complete", "reason": "Succeeded"},
			map[string]interface{}{"lastTransitionTime": "2026-01-02T10:00:00Z", "type": "Progressing", "reason": "Running"},
		}},
	}}
	m.handleRun(ctx, cluster, dyn, runObject)
	updated := &ranv1alpha1.TelcoHealthCheckRun{}
	if err := hub.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: "match"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.AgenticRunActionRequired != "False" || updated.Status.AgenticRunStatus.Type != "Complete" ||
		updated.Status.AgenticRunStatus.Phase != "Succeeded" || updated.Status.AgenticRunStatus.Summary != "healthy" {
		t.Fatalf("independent status updates not reflected: %+v", updated.Status)
	}
	// An AnalysisResult update cannot change AgenticRun type or phase.
	status["actionRequired"] = "True"
	m.handleResult(ctx, cluster, result)
	if err := hub.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: "match"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.AgenticRunStatus.Type != "Complete" || updated.Status.AgenticRunStatus.Phase != "Succeeded" || updated.Status.AgenticRunActionRequired != "False" {
		t.Fatalf("AnalysisResult overwrote run condition: %+v", updated.Status)
	}
	runObject.Object["status"] = map[string]interface{}{"phase": "Running"}
	m.handleRun(ctx, cluster, dyn, runObject)
	if err := hub.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: "match"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.AgenticRunStatus.Type != "Complete" || updated.Status.AgenticRunStatus.Phase != "Succeeded" ||
		updated.Status.AgenticRunActionRequired != "False" {
		t.Fatalf("missing conditions changed existing status: %+v", updated.Status)
	}
	other := &ranv1alpha1.TelcoHealthCheckRun{}
	if err := hub.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: "other"}, other); err != nil {
		t.Fatal(err)
	}
	if other.Status.AgenticRunStatus.Phase != healthcheckrun.PhaseCreated {
		t.Fatalf("AgenticRun updated another cluster: %+v", other.Status)
	}
}

func TestHandleResultActionRequiredWithoutConditions(t *testing.T) {
	ctx := context.Background()
	const cluster, run = "cluster-a", "run-a"
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "match", Namespace: operatorNamespace,
		Labels: map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key(cluster, run)},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{
		ClusterName: cluster, AgenticRunName: run,
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Type: "Analyzed", Phase: healthcheckrun.PhaseCreated},
	}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(record).Build()
	m := newSpokeWatchManager(hub, hub, operatorNamespace)
	result := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec":   map[string]interface{}{"agenticRunName": run},
		"status": map[string]interface{}{"actionRequired": "False"},
	}}
	m.handleResult(ctx, cluster, result)
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), record); err != nil {
		t.Fatal(err)
	}
	if record.Status.AgenticRunStatus.Phase != healthcheckrun.PhaseCreated || record.Status.AgenticRunActionRequired != "False" {
		t.Fatalf("actionRequired not reflected independently: %+v", record.Status)
	}
}

func newResultClient() dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		analysisResultGVR: "AnalysisResultList",
	})
}

func TestActionRequiredFollowsRunType(t *testing.T) {
	ctx := context.Background()
	const cluster, runName = "cluster-a", "run-a"
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "match", Namespace: operatorNamespace,
		Labels: map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key(cluster, runName)},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: cluster, AgenticRunName: runName}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(record).Build()
	m := newSpokeWatchManager(hub, hub, operatorNamespace)
	dyn := newResultClient()
	analysis := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agentic.openshift.io/v1alpha1", "kind": "AnalysisResult",
		"metadata": map[string]interface{}{"name": "analysis-a", "namespace": agenticrun.Namespace},
		"spec":     map[string]interface{}{"agenticRunName": runName},
		"status":   map[string]interface{}{"actionRequired": "True", "diagnosis": map[string]interface{}{"summary": "needs work"}},
	}}
	run := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": runName},
	}}
	setType := func(runType string) {
		t.Helper()
		run.Object["status"] = map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"lastTransitionTime": "2026-01-02T11:00:00Z", "type": runType, "reason": "Running"},
		}}
		m.handleRun(ctx, cluster, dyn, run)
	}
	check := func(want string) {
		t.Helper()
		current := &ranv1alpha1.TelcoHealthCheckRun{}
		if err := hub.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
			t.Fatal(err)
		}
		if current.Status.AgenticRunActionRequired != want {
			t.Fatalf("actionRequired = %q, want %q", current.Status.AgenticRunActionRequired, want)
		}
	}

	if _, err := dyn.Resource(analysisResultGVR).Namespace(agenticrun.Namespace).Create(ctx, analysis, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	m.handleResult(ctx, cluster, analysis)
	check("") // No type yet; analysis cannot assert action required.
	setType("Analyzed")
	check("True")
	setType("Executing")
	check("False")
	m.handleResult(ctx, cluster, analysis)
	check("False")
	setType("Analyzed") // No new result event; recover the stored analysis.
	check("True")
	analysis.Object["status"].(map[string]interface{})["actionRequired"] = "False"
	m.handleResult(ctx, cluster, analysis)
	check("False")
	setType("Complete")
	check("False")
	if _, err := dyn.Resource(analysisResultGVR).Namespace(agenticrun.Namespace).Update(ctx, analysis, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	setType("Analyzed")
	check("False") // Re-entry uses the current analysis, not a prior True.
	setType("")
	check("")
}

func TestRunTypeBeforeAnalysis(t *testing.T) {
	ctx := context.Background()
	const cluster, runName = "cluster-a", "run-a"
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: "match", Namespace: operatorNamespace,
		Labels: map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key(cluster, runName)},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: cluster, AgenticRunName: runName}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(record).Build()
	m := newSpokeWatchManager(hub, hub, operatorNamespace)
	dyn := newResultClient()
	run := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": runName},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"lastTransitionTime": "2026-01-02T11:00:00Z", "type": "Executing", "reason": "Running"},
		}},
	}}
	current := &ranv1alpha1.TelcoHealthCheckRun{}
	m.handleRun(ctx, cluster, dyn, run)
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunActionRequired != "False" {
		t.Fatalf("non-Analyzed type without analysis: actionRequired = %q, want False", current.Status.AgenticRunActionRequired)
	}
	run.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["type"] = "Analyzed"
	m.handleRun(ctx, cluster, dyn, run)
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunActionRequired != "" {
		t.Fatalf("Analyzed without analysis: actionRequired = %q, want unset", current.Status.AgenticRunActionRequired)
	}
	run.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["type"] = "Executing"
	m.handleRun(ctx, cluster, dyn, run)
	analysis := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agentic.openshift.io/v1alpha1", "kind": "AnalysisResult",
		"metadata": map[string]interface{}{"name": "analysis-a", "namespace": agenticrun.Namespace},
		"spec":     map[string]interface{}{"agenticRunName": runName},
		"status":   map[string]interface{}{"actionRequired": "True"},
	}}
	if _, err := dyn.Resource(analysisResultGVR).Namespace(agenticrun.Namespace).Create(ctx, analysis, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	m.handleResult(ctx, cluster, analysis)
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunActionRequired != "False" {
		t.Fatalf("analysis after run advanced: actionRequired = %q, want False", current.Status.AgenticRunActionRequired)
	}
	run.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["type"] = "Analyzed"
	m.handleRun(ctx, cluster, dyn, run)
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunActionRequired != "True" {
		t.Fatalf("returned to Analyzed: actionRequired = %q, want True", current.Status.AgenticRunActionRequired)
	}
	run.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["type"] = "Complete"
	run.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["reason"] = ""
	m.handleRun(ctx, cluster, dyn, run)
	if err := hub.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AgenticRunStatus.Type != "Complete" || current.Status.AgenticRunActionRequired != "False" {
		t.Fatalf("type without reason should clear action: %+v", current.Status)
	}
}

func TestSpokeWatchManagerSyncStartsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	secret := makeKubeconfigSecret("cluster-a")
	secret.Data["kubeconfig"] = []byte(`apiVersion: v1
kind: Config
clusters:
- name: spoke
  cluster:
    server: https://example.com
users:
- name: watcher
  user:
    token: test
contexts:
- name: spoke
  context:
    cluster: spoke
    user: watcher
current-context: spoke
`)
	const runName = "run-a"
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
		Name: runName, Namespace: operatorNamespace,
		Labels: map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key("cluster-a", runName)},
	}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{
		ClusterName: "cluster-a", AgenticRunName: runName,
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhaseCreated},
	}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).WithObjects(secret, record).Build()
	m := newSpokeWatchManager(hub, hub, operatorNamespace)
	m.ctx = ctx
	original := newSpokeDynamicClient
	defer func() { newSpokeDynamicClient = original; m.Stop() }()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		analysisResultGVR: "AnalysisResultList", agenticRunGVR: "AgenticRunList",
	})
	runObject := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agentic.openshift.io/v1alpha1", "kind": "AgenticRun",
		"metadata": map[string]interface{}{"name": runName, "namespace": "openshift-lightspeed"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"lastTransitionTime": "2026-01-02T11:00:00Z", "type": "Complete", "reason": "Succeeded"},
		}},
	}}
	resultObject := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "agentic.openshift.io/v1alpha1", "kind": "AnalysisResult",
		"metadata": map[string]interface{}{"name": "result-a", "namespace": "openshift-lightspeed"},
		"spec":     map[string]interface{}{"agenticRunName": runName},
		"status":   map[string]interface{}{"actionRequired": "True", "diagnosis": map[string]interface{}{"summary": "healthy"}},
	}}
	if _, err := dyn.Resource(agenticRunGVR).Namespace("openshift-lightspeed").Create(ctx, runObject, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(analysisResultGVR).Namespace("openshift-lightspeed").Create(ctx, resultObject, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	newSpokeDynamicClient = func(*rest.Config) (dynamic.Interface, error) { return dyn, nil }
	m.Sync(ctx, []string{"cluster-a"})
	m.mu.Lock()
	count := len(m.watches)
	m.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected a watch, got %d", count)
	}
	// All resource watches must be started for each cluster.
	deadline := time.After(2 * time.Second)
	for {
		seen := map[string]bool{}
		for _, action := range dyn.Actions() {
			if action.GetVerb() == "watch" {
				seen[action.GetResource().Resource] = true
			}
		}
		if len(seen) == 2 && seen[agenticRunGVR.Resource] && seen[analysisResultGVR.Resource] {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("missing spoke watches: %v", seen)
		case <-time.After(10 * time.Millisecond):
		}
	}
	deadline = time.After(2 * time.Second)
	for {
		updated := &ranv1alpha1.TelcoHealthCheckRun{}
		if err := hub.Get(ctx, client.ObjectKeyFromObject(record), updated); err != nil {
			t.Fatal(err)
		}
		if updated.Status.AgenticRunStatus.Type == "Complete" && updated.Status.AgenticRunStatus.Phase == "Succeeded" &&
			updated.Status.AgenticRunStatus.Summary == "healthy" && updated.Status.AgenticRunActionRequired == "False" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("listed spoke objects not reflected: type=%q phase=%q summary=%q actionRequired=%q", updated.Status.AgenticRunStatus.Type, updated.Status.AgenticRunStatus.Phase, updated.Status.AgenticRunStatus.Summary, updated.Status.AgenticRunActionRequired)
		case <-time.After(10 * time.Millisecond):
		}
	}
	// A run returning to Analyzed must recover the analysis value without a new result event.
	runObject.Object["status"].(map[string]interface{})["conditions"].([]interface{})[0].(map[string]interface{})["type"] = "Analyzed"
	if _, err := dyn.Resource(agenticRunGVR).Namespace(agenticrun.Namespace).Update(ctx, runObject, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline = time.After(2 * time.Second)
	for {
		updated := &ranv1alpha1.TelcoHealthCheckRun{}
		if err := hub.Get(ctx, client.ObjectKeyFromObject(record), updated); err != nil {
			t.Fatal(err)
		}
		if updated.Status.AgenticRunActionRequired == "True" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Analyzed transition not reflected: actionRequired=%q", updated.Status.AgenticRunActionRequired)
		case <-time.After(10 * time.Millisecond):
		}
	}
	// A repeated reconcile must not restart an unchanged watch.
	m.Sync(ctx, []string{"cluster-a"})
	m.mu.Lock()
	count = len(m.watches)
	m.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected one watch after repeated sync, got %d", count)
	}
	m.Sync(ctx, nil)
	m.mu.Lock()
	count = len(m.watches)
	m.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected watch cancellation, got %d", count)
	}
}
