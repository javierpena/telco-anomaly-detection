package controller

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

//go:embed assets/purge-cronjob.yaml
var purgeCronJobYAML []byte

const purgeCronJobName = "telco-healthcheck-purge"

func reconcilePurgeCronJob(ctx context.Context, c client.Client, namespace string, thc *ranv1alpha1.TelcoHealthcheck) error {
	if thc.Spec.PurgeInterval == nil {
		return cleanupPurgeCronJob(ctx, c, namespace)
	}
	interval := thc.Spec.PurgeInterval.Duration
	if interval < time.Second || interval%time.Second != 0 {
		return fmt.Errorf("purgeInterval must be a positive whole number of seconds")
	}
	job := &batchv1.CronJob{}
	if err := yaml.UnmarshalStrict(purgeCronJobYAML, job); err != nil {
		return fmt.Errorf("decoding purge CronJob asset: %w", err)
	}
	job.Namespace = namespace
	job.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Env[0].Value = strconv.FormatInt(int64(interval/time.Second), 10)
	job.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: ranv1alpha1.GroupVersion.String(), Kind: "TelcoHealthcheck", Name: thc.Name, UID: thc.UID,
	}}
	existing := &batchv1.CronJob{}
	err := c.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: namespace}, existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, job)
	}
	if err != nil {
		return err
	}
	existing.Labels = job.Labels
	existing.OwnerReferences = job.OwnerReferences
	existing.Spec = job.Spec
	return c.Update(ctx, existing)
}

func cleanupPurgeCronJob(ctx context.Context, c client.Client, namespace string) error {
	job := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: purgeCronJobName, Namespace: namespace}}
	if err := c.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: namespace}, job); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	return client.IgnoreNotFound(c.Delete(ctx, job))
}
