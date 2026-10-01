package controller

import (
	"context"
	"crypto/sha256"
	"reflect"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

var analysisResultGVR = schema.GroupVersionResource{Group: agenticrun.Group, Version: agenticrun.Version, Resource: "analysisresults"}
var agenticRunGVR = schema.GroupVersionResource{Group: agenticrun.Group, Version: agenticrun.Version, Resource: "agenticruns"}

var newSpokeDynamicClient = func(cfg *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(cfg)
}

type spokeWatch struct {
	cancel     context.CancelFunc
	configHash [32]byte
}

// SpokeWatchManager maintains reconnecting AgenticRun and AnalysisResult
// list/watches per monitored cluster. It is a manager runnable so its contexts
// end on shutdown and (when enabled) on leader loss.
type SpokeWatchManager struct {
	hub       client.Client
	reader    client.Reader
	namespace string
	mu        sync.Mutex
	statusMu  sync.Mutex
	ctx       context.Context
	watches   map[string]spokeWatch
}

func newSpokeWatchManager(hub client.Client, reader client.Reader, namespace string) *SpokeWatchManager {
	return &SpokeWatchManager{hub: hub, reader: reader, namespace: namespace, watches: make(map[string]spokeWatch)}
}

func (m *SpokeWatchManager) NeedLeaderElection() bool { return true }

func (m *SpokeWatchManager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx != nil && m.ctx.Err() == nil
}

func (m *SpokeWatchManager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	<-ctx.Done()
	m.Stop()
	return nil
}

func (m *SpokeWatchManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for cluster, entry := range m.watches {
		entry.cancel()
		delete(m.watches, cluster)
	}
}

// Sync starts, stops, or replaces watches as cluster membership or credentials change.
func (m *SpokeWatchManager) Sync(ctx context.Context, clusters []string) {
	wanted := make(map[string]bool, len(clusters))
	for _, cluster := range clusters {
		wanted[cluster] = true
	}
	m.mu.Lock()
	for cluster, entry := range m.watches {
		if !wanted[cluster] {
			entry.cancel()
			delete(m.watches, cluster)
		}
	}
	m.mu.Unlock()
	for _, cluster := range clusters {
		kubeconfig, err := getClusterKubeconfig(ctx, m.hub, cluster)
		if err != nil {
			log.FromContext(ctx).Error(err, "unable to watch spoke runs and results", "cluster", cluster)
			continue
		}
		hash := sha256.Sum256(kubeconfig)
		m.mu.Lock()
		current, exists := m.watches[cluster]
		if exists && current.configHash == hash {
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
		if err != nil {
			log.FromContext(ctx).Error(err, "invalid spoke watch kubeconfig", "cluster", cluster)
			continue
		}
		dyn, err := newSpokeDynamicClient(config)
		if err != nil {
			log.FromContext(ctx).Error(err, "unable to build spoke watch client", "cluster", cluster)
			continue
		}
		m.mu.Lock()
		if m.ctx == nil || m.ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		if exists {
			current.cancel()
		}
		watchCtx, cancel := context.WithCancel(m.ctx)
		m.watches[cluster] = spokeWatch{cancel: cancel, configHash: hash}
		m.mu.Unlock()
		go m.watchCluster(watchCtx, cluster, dyn, agenticRunGVR, func(ctx context.Context, cluster string, run *unstructured.Unstructured) {
			m.handleRun(ctx, cluster, dyn, run)
		})
		go m.watchCluster(watchCtx, cluster, dyn, analysisResultGVR, m.handleResult)
	}
}

type spokeHandler func(context.Context, string, *unstructured.Unstructured)

func (m *SpokeWatchManager) watchCluster(ctx context.Context, cluster string, dyn dynamic.Interface, gvr schema.GroupVersionResource, handle spokeHandler) {
	resource := dyn.Resource(gvr).Namespace(agenticrun.Namespace)
	for ctx.Err() == nil {
		// List before Watch using the list resourceVersion to avoid a gap. Relist
		// after every dropped watch, including expired resourceVersions.
		list, err := resource.List(ctx, metav1.ListOptions{})
		if err == nil {
			for i := range list.Items {
				handle(ctx, cluster, &list.Items[i])
			}
			var stream watch.Interface
			stream, err = resource.Watch(ctx, metav1.ListOptions{ResourceVersion: list.GetResourceVersion(), AllowWatchBookmarks: true})
			if err == nil {
				m.consume(ctx, cluster, stream, handle)
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.FromContext(ctx).Error(err, "spoke watch failed", "cluster", cluster, "resource", gvr.Resource)
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *SpokeWatchManager) consume(ctx context.Context, cluster string, stream watch.Interface, handle spokeHandler) {
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-stream.ResultChan():
			if !ok {
				return
			}
			if event.Type != watch.Added && event.Type != watch.Modified {
				if event.Type == watch.Error {
					return
				}
				continue
			}
			object, ok := event.Object.(*unstructured.Unstructured)
			if ok {
				handle(ctx, cluster, object)
			}
		}
	}
}

func (m *SpokeWatchManager) handleResult(ctx context.Context, cluster string, result *unstructured.Unstructured) {
	runName, _, _ := unstructured.NestedString(result.Object, "spec", "agenticRunName")
	if runName == "" {
		return
	}
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	summary := extractResultSummary(result)
	actionRequired := resultActionRequired(result)
	m.updateMatchingRecordsLocked(ctx, cluster, runName, func(current *ranv1alpha1.TelcoHealthCheckRun) {
		if summary != "" {
			if current.Status.AgenticRunStatus == nil {
				current.Status.AgenticRunStatus = &ranv1alpha1.AgenticRunStatus{}
			}
			current.Status.AgenticRunStatus.Summary = summary
		}
		if current.Status.AgenticRunStatus != nil && current.Status.AgenticRunStatus.Type == "Analyzed" {
			current.Status.AgenticRunActionRequired = actionRequired
		}
	})
}

func resultActionRequired(result *unstructured.Unstructured) string {
	value, _, _ := unstructured.NestedString(result.Object, "status", "actionRequired")
	if value == "True" || value == "False" {
		return value
	}
	return ""
}

func analysisActionRequired(ctx context.Context, dyn dynamic.Interface, runName string) (string, error) {
	results, err := dyn.Resource(analysisResultGVR).Namespace(agenticrun.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	actionRequired := ""
	for i := range results.Items {
		name, _, _ := unstructured.NestedString(results.Items[i].Object, "spec", "agenticRunName")
		if name == runName {
			actionRequired = resultActionRequired(&results.Items[i])
		}
	}
	return actionRequired, nil
}

func (m *SpokeWatchManager) handleRun(ctx context.Context, cluster string, dyn dynamic.Interface, run *unstructured.Unstructured) {
	conditionType, phase := latestCondition(run)
	if (conditionType == "" && phase == "") || run.GetName() == "" {
		return
	}
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	// Re-read analysis on return to Analyzed: its watch may not send another
	// event, and the two streams can be replayed in either order.
	actionRequired := ""
	var analysisErr error
	if conditionType == "Analyzed" {
		actionRequired, analysisErr = analysisActionRequired(ctx, dyn, run.GetName())
		if analysisErr != nil {
			log.FromContext(ctx).Error(analysisErr, "checking AnalysisResults", "cluster", cluster, "run", run.GetName())
		}
	}
	m.updateMatchingRecordsLocked(ctx, cluster, run.GetName(), func(current *ranv1alpha1.TelcoHealthCheckRun) {
		if current.Status.AgenticRunStatus == nil {
			current.Status.AgenticRunStatus = &ranv1alpha1.AgenticRunStatus{}
		}
		current.Status.AgenticRunStatus.Type = conditionType
		current.Status.AgenticRunStatus.Phase = phase
		if conditionType == "" {
			current.Status.AgenticRunActionRequired = ""
		} else if conditionType != "Analyzed" {
			current.Status.AgenticRunActionRequired = "False"
		} else if conditionType == "Analyzed" && analysisErr == nil {
			current.Status.AgenticRunActionRequired = actionRequired
		}
	})
}

// Caller holds statusMu, including while reading the spoke's analysis state.
func (m *SpokeWatchManager) updateMatchingRecordsLocked(ctx context.Context, cluster, runName string, update func(*ranv1alpha1.TelcoHealthCheckRun)) {
	var records ranv1alpha1.TelcoHealthCheckRunList
	if err := m.reader.List(ctx, &records, client.InNamespace(m.namespace),
		client.MatchingLabels{healthcheckrun.RunKeyLabel: healthcheckrun.Key(cluster, runName)}); err != nil {
		log.FromContext(ctx).Error(err, "listing matching hub run records", "cluster", cluster, "run", runName)
		return
	}
	for _, record := range records.Items {
		if record.Status.AgenticRunName != runName || record.Status.ClusterName != cluster {
			continue
		}
		key := types.NamespacedName{Namespace: record.Namespace, Name: record.Name}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &ranv1alpha1.TelcoHealthCheckRun{}
			if err := m.reader.Get(ctx, key, current); err != nil {
				return err
			}
			before := current.DeepCopy()
			// Check the identity again after fetching: a record could have changed
			// between the indexed list and this read.
			if current.Status.AgenticRunName != runName || current.Status.ClusterName != cluster {
				return nil
			}
			update(current)
			if reflect.DeepEqual(current.Status, before.Status) {
				return nil
			}
			queueActionAlert(&current.Status, before.Status)
			return m.hub.Status().Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		}); err != nil {
			log.FromContext(ctx).Error(err, "syncing hub run status", "record", key)
		}
	}
}

// queueActionAlert persists the delivery intent in the same status patch as
// the action change. Replayed watch events do not queue duplicate notifications.
func queueActionAlert(status *ranv1alpha1.TelcoHealthCheckRunStatus, previous ranv1alpha1.TelcoHealthCheckRunStatus) {
	oldAction, newAction := previous.AgenticRunActionRequired, status.AgenticRunActionRequired
	if oldAction == newAction {
		return
	}
	// metav1.Time is serialized at second precision in Kubernetes status.
	// Compare and persist times at the same precision for valid alert ranges.
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
		// Alertmanager requires endsAt after startsAt. Both metav1 timestamps
		// are second-precision, even if the transitions occur milliseconds apart.
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

// latestCondition selects the type and reason of the most recently transitioned
// condition, ignoring conditions without a valid timestamp.
func latestCondition(object *unstructured.Unstructured) (string, string) {
	conditions, _, _ := unstructured.NestedSlice(object.Object, "status", "conditions")
	var newest time.Time
	var conditionType string
	var phase string
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

// extractResultSummary maps the current Lightspeed AnalysisResult schema.
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
