// Package healthcheckrun manages hub-side records of spoke AgenticRuns.
package healthcheckrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

const (
	PendingAnnotation = "ran.openshift.io/spoke-creation-pending"
	RunKeyLabel       = "ran.openshift.io/run-key"
)

// Key is a stable, label-safe identifier for a run on one spoke.
func Key(clusterName, runName string) string {
	sum := sha256.Sum256([]byte(clusterName + "\x00" + runName))
	// Kubernetes label values may not exceed 63 characters. 128 bits of
	// the digest are sufficient to correlate a run across hub and spoke.
	return hex.EncodeToString(sum[:16])
}

// Name uses the spoke run name for alerts and appends the cluster for periodic
// runs, which share a spoke run name across several clusters.
func Name(runName, clusterName string, periodic bool) string {
	if !periodic {
		return runName
	}
	name := runName + "-" + clusterName
	if len(name) <= 253 {
		return name
	}
	return strings.TrimRight(name[:253-17], "-.") + "-" + Key(clusterName, runName)[:16]
}

// Begin persists the run identity before attempting the spoke create. The
// pending annotation lets the controller recover from a process crash or an
// ambiguous network failure without creating a second AgenticRun.
func Begin(ctx context.Context, c client.Client, namespace, name, runName, clusterName string,
	triggeredBy ranv1alpha1.TriggerType, trigger string, owner *ranv1alpha1.TelcoHealthcheck,
) (*ranv1alpha1.TelcoHealthCheckRun, error) {
	if owner.UID == "" {
		return nil, fmt.Errorf("TelcoHealthcheck %q has no UID", owner.Name)
	}
	record := &ranv1alpha1.TelcoHealthCheckRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "telco-anomaly-detection",
				RunKeyLabel:                    Key(clusterName, runName),
			},
			Annotations: map[string]string{PendingAnnotation: "true"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: ranv1alpha1.GroupVersion.String(), Kind: "TelcoHealthcheck",
				Name: owner.Name, UID: owner.UID,
			}},
		},
	}
	if err := c.Create(ctx, record); err != nil {
		return nil, fmt.Errorf("creating pending hub record %s: %w", name, err)
	}
	record.Status = ranv1alpha1.TelcoHealthCheckRunStatus{
		AgenticRunName: runName, ClusterName: clusterName,
		TriggeredBy: triggeredBy, Trigger: trigger,
	}
	if err := c.Status().Update(ctx, record); err != nil {
		// No spoke create can be attempted without a complete durable identity.
		return nil, fmt.Errorf("setting pending hub record status %s: %w", name, err)
	}
	return record, nil
}

// Confirm marks an existing record as paired with a created spoke run.
func Confirm(ctx context.Context, c client.Client, namespace, name string) error {
	record := &ranv1alpha1.TelcoHealthCheckRun{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, record); err != nil {
		return err
	}
	if record.Annotations[PendingAnnotation] != "true" {
		return nil
	}
	before := record.DeepCopy()
	delete(record.Annotations, PendingAnnotation)
	return c.Patch(ctx, record, client.MergeFrom(before))
}
