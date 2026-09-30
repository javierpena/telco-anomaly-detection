package healthcheckrun

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func TestBeginAndConfirm(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ranv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).Build()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName, UID: "owner-uid"}}
	record, err := Begin(context.Background(), c, "telco-healthcheck-system", "run-a", "run-a", "cluster-a", ranv1alpha1.TriggerTypeAlert, "MyAlert", owner)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status.ClusterName != "cluster-a" || record.Annotations[PendingAnnotation] != "true" {
		t.Fatalf("unexpected pending record: %+v", record)
	}
	if record.Status.AgenticRunStatus == nil || record.Status.AgenticRunStatus.Phase != PhasePending || record.Status.AgenticRunActionRequired != "" {
		t.Fatalf("unexpected initial run status: %+v", record.Status)
	}
	serialized, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "agenticRunActionRequired") {
		t.Fatalf("actionRequired must be omitted until reported: %s", serialized)
	}
	if len(record.Labels[RunKeyLabel]) > 63 || record.Labels[RunKeyLabel] != Key("cluster-a", "run-a") {
		t.Fatalf("invalid run correlation label: %q", record.Labels[RunKeyLabel])
	}
	if err := Confirm(context.Background(), c, record.Namespace, record.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(record), record); err != nil {
		t.Fatal(err)
	}
	if record.Annotations[PendingAnnotation] != "" || record.Status.TriggeredBy != ranv1alpha1.TriggerTypeAlert ||
		record.Status.AgenticRunStatus.Phase != PhaseCreated {
		t.Fatalf("unexpected confirmed record: %+v", record)
	}
}

func TestFailRetainsRecord(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ranv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).Build()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName, UID: "owner-uid"}}
	record, err := Begin(ctx, c, "telco-healthcheck-system", "run-b", "run-b", "cluster-a", ranv1alpha1.TriggerTypeAlert, "MyAlert", owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := Fail(ctx, c, record.Namespace, record.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(record), record); err != nil {
		t.Fatal(err)
	}
	if record.Annotations[PendingAnnotation] != "" || record.Status.AgenticRunStatus.Phase != PhaseFailed {
		t.Fatalf("failed record not finalized: %+v", record)
	}
}

func TestConfirmPreservesAnalysisResult(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := ranv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&ranv1alpha1.TelcoHealthCheckRun{}).Build()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName, UID: "owner-uid"}}
	record, err := Begin(ctx, c, "telco-healthcheck-system", "run-c", "run-c", "cluster-a", ranv1alpha1.TriggerTypeAlert, "MyAlert", owner)
	if err != nil {
		t.Fatal(err)
	}
	record.Status.AgenticRunStatus = &ranv1alpha1.AgenticRunStatus{Phase: "NoActionRequired", Summary: "healthy"}
	record.Status.AgenticRunActionRequired = "False"
	if err := c.Status().Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(ctx, c, record.Namespace, record.Name); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(record), record); err != nil {
		t.Fatal(err)
	}
	if record.Status.AgenticRunStatus.Phase != "NoActionRequired" || record.Status.AgenticRunActionRequired != "False" ||
		record.Annotations[PendingAnnotation] != "" {
		t.Fatalf("confirmation overwrote AnalysisResult: %+v", record)
	}
}
