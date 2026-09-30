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
var executionResultGVR = schema.GroupVersionResource{Group: agenticrun.Group, Version: agenticrun.Version, Resource: "executionresults"}
var agenticRunGVR = schema.GroupVersionResource{Group: agenticrun.Group, Version: agenticrun.Version, Resource: "agenticruns"}

var newSpokeDynamicClient = func(cfg *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(cfg)
}

type spokeWatch struct {
	cancel     context.CancelFunc
	configHash [32]byte
}

// SpokeWatchManager maintains reconnecting AgenticRun and result
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
		go m.watchCluster(watchCtx, cluster, dyn, agenticRunGVR, m.handleRun)
		go m.watchCluster(watchCtx, cluster, dyn, analysisResultGVR, func(ctx context.Context, cluster string, result *unstructured.Unstructured) {
			m.handleResult(ctx, cluster, dyn, result)
		})
		go m.watchCluster(watchCtx, cluster, dyn, executionResultGVR, func(ctx context.Context, cluster string, result *unstructured.Unstructured) {
			m.handleExecutionResult(ctx, cluster, dyn, result)
		})
	}
}

type spokeHandler func(context.Context, string, *unstructured.Unstructured)

func (m *SpokeWatchManager) watchCluster(ctx context.Context, cluster string, dyn dynamic.Interface, gvr schema.GroupVersionResource, handle spokeHandler) {
	resource := dyn.Resource(gvr).Namespace(agenticrun.Namespace)
	// Keep the last observed executions so a relist can recover deletions
	// missed while the watch was disconnected.
	seenExecutions := make(map[string]*unstructured.Unstructured)
	for ctx.Err() == nil {
		// List before Watch using the list resourceVersion to avoid a gap. Relist
		// after every dropped watch, including expired resourceVersions.
		list, err := resource.List(ctx, metav1.ListOptions{})
		if err == nil {
			currentExecutions := make(map[string]*unstructured.Unstructured)
			for i := range list.Items {
				object := &list.Items[i]
				if gvr == executionResultGVR {
					currentExecutions[object.GetName()] = object.DeepCopy()
				}
				handle(ctx, cluster, object)
			}
			if gvr == executionResultGVR {
				for name, object := range seenExecutions {
					if _, exists := currentExecutions[name]; !exists {
						handle(ctx, cluster, object)
					}
				}
				seenExecutions = currentExecutions
			}
			var stream watch.Interface
			stream, err = resource.Watch(ctx, metav1.ListOptions{ResourceVersion: list.GetResourceVersion(), AllowWatchBookmarks: true})
			if err == nil {
				m.consume(ctx, cluster, gvr, stream, handle, seenExecutions)
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

func (m *SpokeWatchManager) consume(ctx context.Context, cluster string, gvr schema.GroupVersionResource, stream watch.Interface, handle spokeHandler, seenExecutions map[string]*unstructured.Unstructured) {
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-stream.ResultChan():
			if !ok {
				return
			}
			if event.Type != watch.Added && event.Type != watch.Modified &&
				(event.Type != watch.Deleted || gvr != executionResultGVR) {
				if event.Type == watch.Error {
					return
				}
				continue
			}
			object, ok := event.Object.(*unstructured.Unstructured)
			if ok {
				if gvr == executionResultGVR {
					if event.Type == watch.Deleted {
						delete(seenExecutions, object.GetName())
					} else {
						seenExecutions[object.GetName()] = object.DeepCopy()
					}
				}
				handle(ctx, cluster, object)
			}
		}
	}
}

func (m *SpokeWatchManager) handleResult(ctx context.Context, cluster string, dyn dynamic.Interface, result *unstructured.Unstructured) {
	runName, _, _ := unstructured.NestedString(result.Object, "spec", "agenticRunName")
	if runName == "" {
		return
	}
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	// A relisted AnalysisResult may arrive after an ExecutionResult. Check the
	// spoke rather than relying on the order of independent watch streams.
	hasExecution, err := resultExists(ctx, dyn, executionResultGVR, runName)
	if err != nil {
		log.FromContext(ctx).Error(err, "checking ExecutionResults", "cluster", cluster, "run", runName)
	}
	summary := extractResultSummary(result)
	actionRequired, _, _ := unstructured.NestedString(result.Object, "status", "actionRequired")
	if actionRequired != "True" && actionRequired != "False" {
		actionRequired = ""
	}
	if hasExecution {
		actionRequired = "False"
	}
	m.updateMatchingRecordsLocked(ctx, cluster, runName, func(current *ranv1alpha1.TelcoHealthCheckRun) {
		if summary != "" {
			if current.Status.AgenticRunStatus == nil {
				current.Status.AgenticRunStatus = &ranv1alpha1.AgenticRunStatus{}
			}
			current.Status.AgenticRunStatus.Summary = summary
		}
		// A failed spoke read cannot establish the absence of an execution.
		// Still keep the independent analysis summary up to date.
		if err == nil {
			current.Status.AgenticRunActionRequired = actionRequired
		}
	})
}

// handleExecutionResult recomputes the value on creation, replay, and deletion.
// A deleted result must not clear an action if another execution still exists.
func (m *SpokeWatchManager) handleExecutionResult(ctx context.Context, cluster string, dyn dynamic.Interface, result *unstructured.Unstructured) {
	runName, _, _ := unstructured.NestedString(result.Object, "spec", "agenticRunName")
	if runName == "" {
		return
	}
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	hasExecution, err := resultExists(ctx, dyn, executionResultGVR, runName)
	if err != nil {
		log.FromContext(ctx).Error(err, "checking ExecutionResults", "cluster", cluster, "run", runName)
		return
	}
	actionRequired := "False"
	if !hasExecution {
		// No executions remain: recover the AnalysisResult value, including
		// after a deletion or a dropped watch.
		results, err := dyn.Resource(analysisResultGVR).Namespace(agenticrun.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			log.FromContext(ctx).Error(err, "checking AnalysisResults", "cluster", cluster, "run", runName)
			return
		}
		actionRequired = ""
		for i := range results.Items {
			name, _, _ := unstructured.NestedString(results.Items[i].Object, "spec", "agenticRunName")
			if name != runName {
				continue
			}
			value, _, _ := unstructured.NestedString(results.Items[i].Object, "status", "actionRequired")
			if value == "True" || value == "False" {
				actionRequired = value
			}
		}
	}
	m.updateMatchingRecordsLocked(ctx, cluster, runName, func(current *ranv1alpha1.TelcoHealthCheckRun) {
		current.Status.AgenticRunActionRequired = actionRequired
	})
}

func resultExists(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, runName string) (bool, error) {
	results, err := dyn.Resource(gvr).Namespace(agenticrun.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	for i := range results.Items {
		name, _, _ := unstructured.NestedString(results.Items[i].Object, "spec", "agenticRunName")
		if name == runName {
			return true, nil
		}
	}
	return false, nil
}

func (m *SpokeWatchManager) handleRun(ctx context.Context, cluster string, run *unstructured.Unstructured) {
	conditionType, phase := latestCondition(run)
	if phase == "" || run.GetName() == "" {
		return
	}
	m.updateMatchingRecords(ctx, cluster, run.GetName(), func(current *ranv1alpha1.TelcoHealthCheckRun) {
		if current.Status.AgenticRunStatus == nil {
			current.Status.AgenticRunStatus = &ranv1alpha1.AgenticRunStatus{}
		}
		current.Status.AgenticRunStatus.Type = conditionType
		current.Status.AgenticRunStatus.Phase = phase
	})
}

func (m *SpokeWatchManager) updateMatchingRecords(ctx context.Context, cluster, runName string, update func(*ranv1alpha1.TelcoHealthCheckRun)) {
	// Independent watches may replay the same run concurrently.
	// Serialize their read/patch cycles so each update sees the other's fields.
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	m.updateMatchingRecordsLocked(ctx, cluster, runName, update)
}

// Caller holds statusMu, including while reading the spoke's result state.
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
			return m.hub.Status().Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
		}); err != nil {
			log.FromContext(ctx).Error(err, "syncing hub run status", "record", key)
		}
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
