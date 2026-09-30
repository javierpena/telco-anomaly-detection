package controller

import (
	"context"
	"testing"

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
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

func TestExtractResultStatus(t *testing.T) {
	result := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"lastTransitionTime": "2026-01-02T10:00:00Z", "reason": "Analyzing"},
				map[string]interface{}{"lastTransitionTime": "2026-01-02T11:00:00Z", "reason": "NoActionRequired"},
			},
			"diagnosis": map[string]interface{}{"summary": "no issue found"},
		},
	}}
	got := extractResultStatus(result)
	if got == nil || got.Phase != "NoActionRequired" || got.Summary != "no issue found" {
		t.Fatalf("unexpected result: %+v", got)
	}
	result.Object["status"] = map[string]interface{}{"options": []interface{}{map[string]interface{}{"diagnosis": map[string]interface{}{"summary": "fix needed"}}}}
	got = extractResultStatus(result)
	if got == nil || got.Summary != "fix needed" {
		t.Fatalf("missing option diagnosis: %+v", got)
	}
}

func TestHandleResultMatchesClusterAndRun(t *testing.T) {
	ctx := context.Background()
	const cluster, run = "cluster-a", "run-a"
	makeRecord := func(name, clusterName string) *ranv1alpha1.TelcoHealthCheckRun {
		return &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: operatorNamespace, Labels: map[string]string{healthcheckrun.RunKeyLabel: healthcheckrun.Key(clusterName, run)},
		}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: clusterName, AgenticRunName: run,
			AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhaseCreated}}}
	}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).
		WithObjects(makeRecord("match", cluster), makeRecord("other", "cluster-b")).Build()
	m := newSpokeWatchManager(hub, hub, operatorNamespace)
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
		if tt.want && (record.Status.AgenticRunStatus.Phase != "Completed" ||
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
	updated := &ranv1alpha1.TelcoHealthCheckRun{}
	if err := hub.Get(ctx, client.ObjectKey{Namespace: operatorNamespace, Name: "match"}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.AgenticRunActionRequired != "False" || updated.Status.AgenticRunStatus.Phase != "NoActionRequired" {
		t.Fatalf("AnalysisResult update not reflected: %+v", updated.Status)
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
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Phase: healthcheckrun.PhaseCreated},
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
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(secret).Build()
	m := newSpokeWatchManager(hub, hub, operatorNamespace)
	m.ctx = ctx
	original := newSpokeDynamicClient
	defer func() { newSpokeDynamicClient = original; m.Stop() }()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{analysisResultGVR: "AnalysisResultList"})
	newSpokeDynamicClient = func(*rest.Config) (dynamic.Interface, error) { return dyn, nil }
	m.Sync(ctx, []string{"cluster-a"})
	m.mu.Lock()
	count := len(m.watches)
	m.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected a watch, got %d", count)
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
