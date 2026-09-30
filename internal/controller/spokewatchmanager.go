package controller

import (
	"context"
	"crypto/sha256"
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

var newSpokeDynamicClient = func(cfg *rest.Config) (dynamic.Interface, error) {
	return dynamic.NewForConfig(cfg)
}

type spokeWatch struct {
	cancel     context.CancelFunc
	configHash [32]byte
}

// SpokeWatchManager maintains one reconnecting AnalysisResult list/watch per
// monitored cluster. It is a manager runnable so its contexts end on shutdown
// and (when enabled) on leader loss.
type SpokeWatchManager struct {
	hub       client.Client
	reader    client.Reader
	namespace string
	mu        sync.Mutex
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
			log.FromContext(ctx).Error(err, "unable to watch spoke results", "cluster", cluster)
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
		go m.watchCluster(watchCtx, cluster, dyn)
	}
}

func (m *SpokeWatchManager) watchCluster(ctx context.Context, cluster string, dyn dynamic.Interface) {
	resource := dyn.Resource(analysisResultGVR).Namespace(agenticrun.Namespace)
	for ctx.Err() == nil {
		// List before Watch using the list resourceVersion to avoid a gap. Relist
		// after every dropped watch, including expired resourceVersions.
		list, err := resource.List(ctx, metav1.ListOptions{})
		if err == nil {
			for i := range list.Items {
				m.handleResult(ctx, cluster, &list.Items[i])
			}
			var stream watch.Interface
			stream, err = resource.Watch(ctx, metav1.ListOptions{ResourceVersion: list.GetResourceVersion(), AllowWatchBookmarks: true})
			if err == nil {
				m.consume(ctx, cluster, stream)
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.FromContext(ctx).Error(err, "spoke AnalysisResult watch failed", "cluster", cluster)
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

func (m *SpokeWatchManager) consume(ctx context.Context, cluster string, stream watch.Interface) {
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
			result, ok := event.Object.(*unstructured.Unstructured)
			if ok {
				m.handleResult(ctx, cluster, result)
			}
		}
	}
}

func (m *SpokeWatchManager) handleResult(ctx context.Context, cluster string, result *unstructured.Unstructured) {
	runName, _, _ := unstructured.NestedString(result.Object, "spec", "agenticRunName")
	if runName == "" {
		return
	}
	observed := extractResultStatus(result)
	if observed == nil {
		return
	}
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
			if current.Status.AgenticRunStatus != nil && *current.Status.AgenticRunStatus == *observed {
				return nil
			}
			before := current.DeepCopy()
			current.Status.AgenticRunStatus = observed
			return m.hub.Status().Patch(ctx, current, client.MergeFrom(before))
		}); err != nil {
			log.FromContext(ctx).Error(err, "syncing hub run result", "record", key)
		}
	}
}

// extractResultStatus maps the current Lightspeed AnalysisResult schema. There
// is no .status.phase or .status.summary on that CRD.
func extractResultStatus(result *unstructured.Unstructured) *ranv1alpha1.AgenticRunStatus {
	conditions, _, _ := unstructured.NestedSlice(result.Object, "status", "conditions")
	var newest time.Time
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
			phase, _ = condition["reason"].(string)
		}
	}
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
	if phase == "" && summary == "" {
		return nil
	}
	return &ranv1alpha1.AgenticRunStatus{Phase: phase, Summary: summary}
}
