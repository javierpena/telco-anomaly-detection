package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

func HubRunObject(kind string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: agenticrun.Group, Version: agenticrun.Version, Kind: kind})
	return u
}

func hubRunObject(kind string) *unstructured.Unstructured { return HubRunObject(kind) }

func hubRunList(kind string) *unstructured.UnstructuredList {
	u := &unstructured.UnstructuredList{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: agenticrun.Group, Version: agenticrun.Version, Kind: kind + "List"})
	return u
}

// syncHubRunStatus re-reads the entire current run/result state. All event
// streams (including record creation) converge through this one operation.
func (r *runAlertReconciler) syncHubRunStatus(ctx context.Context, record *ranv1alpha1.TelcoHealthCheckRun) error {
	if record.Annotations[healthcheckrun.HubOriginAnnotation] != "true" ||
		record.Status.AgenticRunName == "" || record.Status.ClusterName == "" {
		return nil
	}
	run := hubRunObject(agenticrun.Kind)
	key := client.ObjectKey{Namespace: agenticrun.Namespace, Name: record.Status.AgenticRunName}
	if err := r.reader.Get(ctx, key, run); err != nil {
		// A missing run may still be pending creation, or may have expired
		// after completion. Recovery handles pending creates separately.
		return client.IgnoreNotFound(err)
	}
	cluster, _, err := unstructured.NestedString(run.Object, "spec", "targetCluster")
	if err != nil || cluster != record.Status.ClusterName || run.GetLabels()["app.kubernetes.io/managed-by"] != "telco-anomaly-detection" ||
		record.Labels[healthcheckrun.RunKeyLabel] != healthcheckrun.Key(cluster, run.GetName()) {
		return fmt.Errorf("hub AgenticRun %s/%s does not match run record %s/%s", key.Namespace, key.Name, record.Namespace, record.Name)
	}

	conditionType, phase := latestCondition(run)
	var summary, action string
	refs, _, err := unstructured.NestedSlice(run.Object, "status", "steps", "analysis", "results")
	if err != nil {
		return fmt.Errorf("reading analysis result references: %w", err)
	}
	if len(refs) > 0 {
		last, ok := refs[len(refs)-1].(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid latest analysis result reference on %s", run.GetName())
		}
		resultName, ok := last["name"].(string)
		if !ok || resultName == "" {
			return fmt.Errorf("missing latest analysis result name on %s", run.GetName())
		}
		result := hubRunObject("AnalysisResult")
		if err := r.reader.Get(ctx, client.ObjectKey{Namespace: agenticrun.Namespace, Name: resultName}, result); err != nil {
			return fmt.Errorf("reading latest AnalysisResult %s: %w", resultName, err)
		}
		parent, _, err := unstructured.NestedString(result.Object, "spec", "agenticRunName")
		if err != nil || parent != run.GetName() {
			return fmt.Errorf("AnalysisResult %s has mismatched parent", resultName)
		}
		if run.GetUID() != "" {
			owned := false
			for _, ref := range result.GetOwnerReferences() {
				if ref.Kind == agenticrun.Kind && ref.UID == run.GetUID() {
					owned = true
					break
				}
			}
			if !owned {
				return fmt.Errorf("AnalysisResult %s is not owned by hub run %s", resultName, run.GetName())
			}
		}
		summary, action = extractResultSummary(result), resultActionRequired(result)
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &ranv1alpha1.TelcoHealthCheckRun{}
		if err := r.reader.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if current.Annotations[healthcheckrun.HubOriginAnnotation] != "true" ||
			current.Status.ClusterName != cluster || current.Status.AgenticRunName != run.GetName() ||
			current.Labels[healthcheckrun.RunKeyLabel] != healthcheckrun.Key(cluster, run.GetName()) {
			return nil
		}
		before := current.DeepCopy()
		if current.Status.AgenticRunStatus == nil {
			current.Status.AgenticRunStatus = &ranv1alpha1.AgenticRunStatus{}
		}
		if conditionType != "" {
			current.Status.AgenticRunStatus.Type = conditionType
			current.Status.AgenticRunStatus.Phase = phase
			if conditionType != "Analyzed" {
				current.Status.AgenticRunActionRequired = "False"
			} else {
				current.Status.AgenticRunActionRequired = action
			}
		}
		if summary != "" {
			current.Status.AgenticRunStatus.Summary = summary
		}
		if reflect.DeepEqual(current.Status, before.Status) {
			return nil
		}
		queueActionAlert(&current.Status, before.Status)
		return r.Status().Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
}

func resultActionRequired(result *unstructured.Unstructured) string {
	value, _, _ := unstructured.NestedString(result.Object, "status", "actionRequired")
	if value == "True" || value == "False" {
		return value
	}
	return ""
}

// queueActionAlert persists delivery intent in the same status patch as the
// action change. Replayed events do not queue duplicate notifications.
func queueActionAlert(status *ranv1alpha1.TelcoHealthCheckRunStatus, previous ranv1alpha1.TelcoHealthCheckRunStatus) {
	oldAction, newAction := previous.AgenticRunActionRequired, status.AgenticRunActionRequired
	if oldAction == newAction {
		return
	}
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	summary := ""
	if status.AgenticRunStatus != nil {
		summary = status.AgenticRunStatus.Summary
	}
	if newAction == "True" {
		status.AlertStartsAt = &now
		status.LastAlertSentTime = nil
		status.AlertNotifications = append(status.AlertNotifications, ranv1alpha1.AlertNotification{
			Firing: true, StartsAt: now, Summary: summary,
		})
	} else if oldAction == "True" {
		startsAt := now
		if status.AlertStartsAt != nil {
			startsAt = *status.AlertStartsAt
		}
		if !now.After(startsAt.Time) {
			now = metav1.NewTime(startsAt.Add(time.Second))
		}
		status.AlertNotifications = append(status.AlertNotifications, ranv1alpha1.AlertNotification{
			StartsAt: startsAt, EndsAt: &now, Summary: summary,
		})
		status.AlertStartsAt = nil
		status.LastAlertSentTime = nil
	}
}

// latestCondition uses the reason of the most recently transitioned condition
// as the record phase (not AgenticRun's derived lifecycle phase).
func latestCondition(object *unstructured.Unstructured) (string, string) {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	var newest time.Time
	var conditionType, phase string
	for _, item := range conditions {
		condition, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		stamp, _ := condition["lastTransitionTime"].(string)
		when, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			continue
		}
		if !when.Before(newest) {
			newest = when
			conditionType, _ = condition["type"].(string)
			phase, _ = condition["reason"].(string)
		}
	}
	return conditionType, phase
}

func extractResultSummary(result *unstructured.Unstructured) string {
	summary, _, _ := unstructured.NestedString(result.Object, "status", "diagnosis", "summary")
	if summary == "" {
		options, _, _ := unstructured.NestedSlice(result.Object, "status", "options")
		for _, item := range options {
			option, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			summary, _, _ = unstructured.NestedString(option, "diagnosis", "summary")
			if summary == "" {
				summary, _, _ = unstructured.NestedString(option, "summary")
			}
			if summary != "" {
				break
			}
		}
	}
	if summary == "" {
		summary, _, _ = unstructured.NestedString(result.Object, "status", "failureReason")
	}
	return summary
}
