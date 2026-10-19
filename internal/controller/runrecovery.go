package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

const pendingRunGrace = 2 * time.Minute

// recoverPendingRuns checks whether hub-origin pending records have a corresponding
// hub run. A network error during Create is ambiguous, so never recreate the run.
// A stale record with no corresponding run is retained as Failed.
func recoverPendingRuns(ctx context.Context, c client.Client, namespace string) (bool, error) {
	var records ranv1alpha1.TelcoHealthCheckRunList
	if err := c.List(ctx, &records, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("listing run records for recovery: %w", err)
	}
	pending := false
	for i := range records.Items {
		record := &records.Items[i]
		if record.Annotations[healthcheckrun.PendingAnnotation] != "true" ||
			record.Annotations[healthcheckrun.HubOriginAnnotation] != "true" {
			continue
		}
		pending = true
		if record.CreationTimestamp.IsZero() || time.Since(record.CreationTimestamp.Time) < pendingRunGrace {
			continue
		}
		if record.Status.ClusterName == "" || record.Status.AgenticRunName == "" {
			if err := c.Delete(ctx, record); err != nil && !apierrors.IsNotFound(err) {
				log.FromContext(ctx).Error(err, "removing incomplete pending record", "name", record.Name)
			}
			continue
		}
		run := &unstructured.Unstructured{}
		run.SetGroupVersionKind(schema.GroupVersionKind{Group: agenticrun.Group, Version: agenticrun.Version, Kind: agenticrun.Kind})
		err := c.Get(ctx, client.ObjectKey{Namespace: agenticrun.Namespace, Name: record.Status.AgenticRunName}, run)
		if apierrors.IsNotFound(err) {
			if err := healthcheckrun.Fail(ctx, c, namespace, record.Name); err != nil {
				log.FromContext(ctx).Error(err, "pending run recovery: marking creation failed", "name", record.Name)
			}
			continue
		}
		if err != nil {
			log.FromContext(ctx).Error(err, "pending run recovery: checking hub run", "name", record.Name)
			continue
		}
		cluster, _, err := unstructured.NestedString(run.Object, "spec", "targetCluster")
		if err != nil || cluster != record.Status.ClusterName ||
			record.Labels[healthcheckrun.RunKeyLabel] != healthcheckrun.Key(cluster, run.GetName()) ||
			run.GetLabels()["app.kubernetes.io/managed-by"] != "telco-anomaly-detection" {
			log.FromContext(ctx).Info("pending run recovery: hub run identity mismatch", "name", record.Name)
			continue
		}
		if err := healthcheckrun.Confirm(ctx, c, namespace, record.Name); err != nil {
			log.FromContext(ctx).Error(err, "pending run recovery: confirming record", "name", record.Name)
		}
	}
	return pending, nil
}
