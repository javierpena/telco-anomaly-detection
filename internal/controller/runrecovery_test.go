package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

func TestRecoverPendingRuns(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "created on spoke", false: "never created"}[present], func(t *testing.T) {
			ctx := context.Background()
			scheme := newTestScheme(t)
			record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{
				Name: "run-a", Namespace: operatorNamespace,
				CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
				Annotations:       map[string]string{healthcheckrun.PendingAnnotation: "true"},
			}, Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "cluster-a", AgenticRunName: "run-a"}}
			hub := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(record).WithObjects(record, makeKubeconfigSecret("cluster-a")).Build()
			spoke := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
			if present {
				run := &unstructured.Unstructured{}
				run.SetAPIVersion(agenticrun.APIVersion)
				run.SetKind(agenticrun.Kind)
				run.SetName("run-a")
				run.SetNamespace(agenticrun.Namespace)
				if err := spoke.Create(ctx, run); err != nil {
					t.Fatal(err)
				}
			}
			original := buildSpokeClient
			defer func() { buildSpokeClient = original }()
			buildSpokeClient = func([]byte) (client.Client, error) { return spoke, nil }
			pending, err := recoverPendingRuns(ctx, hub, operatorNamespace)
			if err != nil || !pending {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			got := &ranv1alpha1.TelcoHealthCheckRun{}
			err = hub.Get(ctx, client.ObjectKeyFromObject(record), got)
			if present {
				if err != nil || got.Annotations[healthcheckrun.PendingAnnotation] != "" {
					t.Fatalf("record not confirmed: %+v, %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("orphan record not removed: %+v", got)
			}
		})
	}
}
