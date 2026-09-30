package controller

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func TestReconcilePurgeCronJob(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).Build()
	thc := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName, UID: "owner"}}
	interval := metav1.Duration{Duration: 168 * time.Hour}
	thc.Spec.PurgeInterval = &interval
	if err := reconcilePurgeCronJob(ctx, c, operatorNamespace, thc); err != nil {
		t.Fatal(err)
	}
	job := &batchv1.CronJob{}
	key := client.ObjectKey{Namespace: operatorNamespace, Name: purgeCronJobName}
	if err := c.Get(ctx, key, job); err != nil {
		t.Fatal(err)
	}
	if job.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName != "telco-anomaly-operator" ||
		job.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Env[0].Value != "604800" ||
		job.OwnerReferences[0].UID != thc.UID {
		t.Fatalf("unexpected purge job: %+v", job)
	}
	interval.Duration = 24 * time.Hour
	if err := reconcilePurgeCronJob(ctx, c, operatorNamespace, thc); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, job); err != nil {
		t.Fatal(err)
	}
	if job.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Env[0].Value != "86400" {
		t.Fatal("purge interval not updated")
	}
	thc.Spec.PurgeInterval = nil
	if err := reconcilePurgeCronJob(ctx, c, operatorNamespace, thc); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, job); err == nil {
		t.Fatal("expected CronJob deletion")
	}
}
