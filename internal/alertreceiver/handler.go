// Package alertreceiver implements the HTTP webhook server that receives AlertManager
// notifications and triggers agentic runs on matched managed clusters.
package alertreceiver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

const (
	operatorNamespace = "telco-healthcheck-system"

	thanosRulerConfigMap   = "thanos-ruler-custom-rules"
	customRulesKey         = "custom_rules.yaml"
	observabilityNamespace = "open-cluster-management-observability"

	// Labels that identify alert ConfigMaps (duplicated from the controller package
	// to avoid a cross-binary import dependency).
	userAlertManagedByLabel = "app.kubernetes.io/managed-by"
	userAlertManagedByValue = "telco-anomaly-detection"
	userAlertLabel          = "ran.openshift.io/user-managed-alert"
	userAlertLabelValue     = "true"
	systemAlertLabel        = "ran.openshift.io/system-managed-alert"
	systemAlertLabelValue   = "true"
)

// AlertManagerPayload is the top-level payload sent by AlertManager webhooks.
type AlertManagerPayload struct {
	Version  string  `json:"version"`
	Status   string  `json:"status"`
	Receiver string  `json:"receiver"`
	Alerts   []Alert `json:"alerts"`
}

// Alert represents a single alert within an AlertManager webhook payload.
type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

// Handler processes incoming AlertManager webhook payloads.
// It validates each alert against the list of monitored clusters and defined alert names,
// then creates an AgenticRun on the matching managed cluster.
type Handler struct {
	// HubClient reads hub configuration and creates hub AgenticRuns and records.
	HubClient client.Client
	// LogLevel, when non-nil, is updated on each webhook call to reflect the most
	// verbose logLevel across all TelcoHealthcheck CRs.
	LogLevel *uberzap.AtomicLevel
}

// NewHandler creates a Handler with default dependencies.
func NewHandler(hubClient client.Client, logLevel *uberzap.AtomicLevel) *Handler {
	return &Handler{
		HubClient: hubClient,
		LogLevel:  logLevel,
	}
}

// HandleWebhook processes a POST /webhook request from AlertManager.
func (h *Handler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		logger.Error(err, "failed to read webhook body")
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var payload AlertManagerPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.Error(err, "failed to parse AlertManager payload")
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	logger.Info("received AlertManager webhook",
		"status", payload.Status,
		"alertCount", len(payload.Alerts),
		"receiver", payload.Receiver)

	if err := h.processAlerts(ctx, payload.Alerts); err != nil {
		logger.Error(err, "error processing alerts")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// processAlerts iterates over the alerts in a payload and creates AgenticRun resources
// for those that match a monitored cluster and a defined alert name.
func (h *Handler) processAlerts(ctx context.Context, alerts []Alert) error {
	if len(alerts) == 0 {
		return nil
	}

	logger := log.FromContext(ctx)

	monitoredClusters, err := h.getMonitoredClusters(ctx)
	if err != nil {
		return fmt.Errorf("fetching monitored clusters: %w", err)
	}
	logger.V(1).Info("loaded monitored clusters", "count", len(monitoredClusters))

	alertNames, err := h.getDefinedAlertNames(ctx)
	if err != nil {
		// Non-fatal: if we can't load alert names, skip alert-name validation
		// but still log the issue.
		logger.Error(err, "failed to load defined alert names; skipping all alerts")
		return nil
	}
	logger.V(1).Info("loaded defined alert names", "count", len(alertNames))

	for _, alert := range alerts {
		clusterName := alert.Labels["cluster"]
		alertName := alert.Labels["alertname"]

		logger.V(1).Info("evaluating alert",
			"cluster", clusterName,
			"alertname", alertName,
			"status", alert.Status)

		if alert.Status != "firing" {
			logger.V(1).Info("alert is not firing, skipping",
				"cluster", clusterName, "alertname", alertName, "status", alert.Status)
			continue
		}
		if !monitoredClusters[clusterName] {
			logger.V(1).Info("alert cluster not monitored, skipping",
				"cluster", clusterName)
			continue
		}
		if !alertNames[alertName] {
			logger.V(1).Info("alert name not in defined rules, skipping",
				"alertname", alertName)
			continue
		}

		logger.Info("matched alert – creating AgenticRun",
			"cluster", clusterName,
			"alertname", alertName)

		if err := h.createAgenticRunOnCluster(ctx, clusterName, alertName, alert.Annotations); err != nil {
			logger.Error(err, "failed to create AgenticRun for alert",
				"cluster", clusterName, "alertname", alertName)
			// Continue processing other alerts.
		}
	}
	return nil
}

// getMonitoredClusters reads the singleton TelcoHealthcheck CR and returns the set of monitored
// cluster names.
func (h *Handler) getMonitoredClusters(ctx context.Context) (map[string]bool, error) {
	thc := &ranv1alpha1.TelcoHealthcheck{}
	if err := h.HubClient.Get(ctx, client.ObjectKey{
		Name: ranv1alpha1.TelcoHealthcheckCanonicalName,
	}, thc); err != nil {
		return nil, fmt.Errorf("getting TelcoHealthcheck: %w", err)
	}

	if h.LogLevel != nil {
		level := zapcore.InfoLevel
		if thc.Spec.LogLevel == ranv1alpha1.LogLevelDebug {
			level = zapcore.DebugLevel
		}
		h.LogLevel.SetLevel(level)
	}

	clusterSet := make(map[string]bool, len(thc.Status.MonitoredClusters))
	for _, name := range thc.Status.MonitoredClusters {
		clusterSet[name] = true
	}

	return clusterSet, nil
}

// getDefinedAlertNames reads the thanos-ruler-custom-rules ConfigMap and extracts
// all alert names from the rules YAML.
func (h *Handler) getDefinedAlertNames(ctx context.Context) (map[string]bool, error) {
	// Import the ConfigMap type inline via unstructured to avoid a dependency on the controller package.
	cm := &unstructured.Unstructured{}
	cm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "ConfigMap",
	})
	if err := h.HubClient.Get(ctx, client.ObjectKey{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		return nil, fmt.Errorf("getting Thanos ConfigMap: %w", err)
	}

	data, _, _ := unstructured.NestedStringMap(cm.Object, "data")
	rulesYAML := data[customRulesKey]
	return parseAlertNamesFromRulesYAML(rulesYAML), nil
}

// parseAlertNamesFromRulesYAML scans a Prometheus rules YAML string for "alert: <name>" lines.
func parseAlertNamesFromRulesYAML(input string) map[string]bool {
	names := make(map[string]bool)
	i := 0
	for i < len(input) {
		j := i
		for j < len(input) && input[j] != '\n' {
			j++
		}
		line := input[i:j]
		if name := extractAlertName(line); name != "" {
			names[name] = true
		}
		i = j + 1
	}
	return names
}

// extractAlertName extracts the value after "alert:" in a line.
func extractAlertName(line string) string {
	const prefix = "alert:"
	for k := 0; k+len(prefix) <= len(line); k++ {
		if line[k:k+len(prefix)] == prefix {
			rest := line[k+len(prefix):]
			start := 0
			for start < len(rest) && (rest[start] == ' ' || rest[start] == '\t') {
				start++
			}
			if name := rest[start:]; len(name) > 0 {
				return name
			}
		}
	}
	return ""
}

// resolveAlertConfigMap returns the ConfigMap name that holds AgenticRun config for alertName.
// It lists all managed ConfigMaps in namespace and matches by alertName data field,
// accepting both system-alert (ran.openshift.io/system-managed-alert) and user-alert
// (ran.openshift.io/user-managed-alert) ConfigMaps in a single pass.
func resolveAlertConfigMap(ctx context.Context, c client.Client, alertName, namespace string) (string, error) {
	var cmList corev1.ConfigMapList
	if err := c.List(ctx, &cmList,
		client.InNamespace(namespace),
		client.MatchingLabels{userAlertManagedByLabel: userAlertManagedByValue},
	); err != nil {
		return "", err
	}
	for _, cm := range cmList.Items {
		isSystem := cm.Labels[systemAlertLabel] == systemAlertLabelValue
		isUser := cm.Labels[userAlertLabel] == userAlertLabelValue
		if (isSystem || isUser) && cm.Data["alertName"] == alertName {
			return cm.Name, nil
		}
	}
	return "", fmt.Errorf("no AgenticRun config found for alert %q", alertName)
}

// createAgenticRunOnCluster creates an AgenticRun in the openshift-lightspeed namespace
// on the hub targeting the named managed cluster.
func (h *Handler) createAgenticRunOnCluster(ctx context.Context, clusterName, alertName string, annotations map[string]string) error {
	logger := log.FromContext(ctx)

	configMapName, err := resolveAlertConfigMap(ctx, h.HubClient, alertName, operatorNamespace)
	if err != nil {
		logger.Error(err, "no AgenticRun ConfigMap for alert, skipping", "alertname", alertName)
		return nil
	}

	cfg, err := agenticrun.LoadRunConfig(ctx, h.HubClient, configMapName, operatorNamespace)
	if err != nil {
		return fmt.Errorf("loading AgenticRun config for alert %q: %w", alertName, err)
	}
	if cfg.Request == "" {
		logger.Error(nil, "AgenticRun config has empty request field, skipping", "alertname", alertName, "configMap", configMapName)
		return nil
	}

	vars, err := agenticrun.BuildVarMap(ctx, h.HubClient, operatorNamespace, clusterName)
	if err != nil {
		return fmt.Errorf("building variable map for alert %q on cluster %s: %w", alertName, clusterName, err)
	}
	if node := annotations["node"]; node != "" {
		vars["NODE_NAME"] = node
	}
	cfg = agenticrun.ExpandVariables(cfg, vars)

	runName := fmt.Sprintf("telco-alert-%s-%d", strings.ToLower(alertName), time.Now().UnixNano())

	labels := map[string]string{
		"app.kubernetes.io/managed-by":     "telco-anomaly-detection",
		"telco-anomaly.io/trigger-type":    "alert",
		"telco-anomaly.io/trigger-alert":   alertName,
		"telco-anomaly.io/trigger-cluster": clusterName,
	}
	u, err := agenticrun.BuildObject(runName, clusterName, labels, cfg)
	if err != nil {
		return fmt.Errorf("building AgenticRun object: %w", err)
	}
	owner := &ranv1alpha1.TelcoHealthcheck{}
	if err := h.HubClient.Get(ctx, client.ObjectKey{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}, owner); err != nil {
		return fmt.Errorf("fetching singleton for audit record before hub creation: %w", err)
	}
	if _, err := healthcheckrun.Begin(ctx, h.HubClient, operatorNamespace, runName, runName,
		clusterName, ranv1alpha1.TriggerTypeAlert, alertName, owner); err != nil {
		return err
	}

	if err := h.HubClient.Create(ctx, u); err != nil {
		if healthcheckrun.DefinitiveCreateError(err) {
			if statusErr := healthcheckrun.Fail(ctx, h.HubClient, operatorNamespace, runName); statusErr != nil {
				logger.Error(statusErr, "failed to record hub creation failure; pending record will be checked", "cluster", clusterName, "name", runName)
			}
		}
		return fmt.Errorf("creating AgenticRun %s on cluster %s (hub record retained): %w", runName, clusterName, err)
	}
	if err := healthcheckrun.Confirm(ctx, h.HubClient, operatorNamespace, runName); err != nil {
		logger.Error(err, "failed to confirm hub record; controller will retry", "cluster", clusterName, "name", runName)
	}

	logger.Info("created AgenticRun for alert",
		"cluster", clusterName,
		"alertname", alertName,
		"agenticRunName", runName)
	return nil
}
