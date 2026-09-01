// Package alertreceiver implements the HTTP webhook server that receives AlertManager
// notifications and triggers agentic runs on matched managed clusters.
package alertreceiver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
)

const (
	operatorNamespace = "telco-healthcheck-system"

	thanosRulerConfigMap   = "thanos-ruler-custom-rules"
	customRulesKey         = "custom_rules.yaml"
	observabilityNamespace = "open-cluster-management-observability"
)

// alertConfigMaps maps an AlertManager alert name to the ConfigMap that holds its AgenticRun config.
var alertConfigMaps = map[string]string{
	"TelcoHealthCheckHostNetwork":    "telco-anomaly-host-network-config",
	"TelcoHealthCheckPodNetwork":     "telco-anomaly-pod-network-config",
	"TelcoHealthCheckHostReservedCPU": "telco-anomaly-host-reserved-cpu-config",
}

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
	// HubClient is a client for the ACM hub cluster (reads TelcoHealthcheck, Secrets).
	HubClient client.Client
	// NewSpokeClient builds a client for a spoke cluster from kubeconfig bytes.
	// Overridable for testing.
	NewSpokeClient func([]byte) (client.Client, error)
	// LogLevel, when non-nil, is updated on each webhook call to reflect the most
	// verbose logLevel across all TelcoHealthcheck CRs.
	LogLevel *uberzap.AtomicLevel
}

// NewHandler creates a Handler with default dependencies.
func NewHandler(hubClient client.Client, logLevel *uberzap.AtomicLevel) *Handler {
	return &Handler{
		HubClient:      hubClient,
		NewSpokeClient: defaultNewSpokeClient,
		LogLevel:       logLevel,
	}
}

func defaultNewSpokeClient(kubeconfigBytes []byte) (client.Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("building REST config: %w", err)
	}
	return newClientFromConfig(cfg)
}

// newClientFromConfig is overridable for testing.
var newClientFromConfig = func(cfg *rest.Config) (client.Client, error) {
	return client.New(cfg, client.Options{})
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

	monitoredClusters, clusterKubeconfigs, err := h.getMonitoredClusters(ctx)
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

		kubeconfig := clusterKubeconfigs[clusterName]
		if err := h.createAgenticRunOnCluster(ctx, kubeconfig, clusterName, alertName); err != nil {
			logger.Error(err, "failed to create AgenticRun for alert",
				"cluster", clusterName, "alertname", alertName)
			// Continue processing other alerts.
		}
	}
	return nil
}

// getMonitoredClusters reads all TelcoHealthcheck CRs and builds the union of monitored
// cluster names along with their kubeconfig bytes.
func (h *Handler) getMonitoredClusters(ctx context.Context) (map[string]bool, map[string][]byte, error) {
	thcList := &ranv1alpha1.TelcoHealthcheckList{}
	if err := h.HubClient.List(ctx, thcList); err != nil {
		return nil, nil, fmt.Errorf("listing TelcoHealthchecks: %w", err)
	}

	// Sync log level from CRs — reuses the already-fetched list, no extra API call.
	if h.LogLevel != nil {
		level := zapcore.InfoLevel
		if ranv1alpha1.IsDebugLevel(thcList.Items) {
			level = zapcore.DebugLevel
		}
		h.LogLevel.SetLevel(level)
	}

	clusterSet := make(map[string]bool)
	for _, thc := range thcList.Items {
		for _, name := range thc.Status.MonitoredClusters {
			clusterSet[name] = true
		}
	}

	kubeconfigs := make(map[string][]byte, len(clusterSet))
	for clusterName := range clusterSet {
		kubeconfig, err := fetchKubeconfig(ctx, h.HubClient, clusterName)
		if err != nil {
			log.FromContext(ctx).Error(err, "failed to fetch kubeconfig", "cluster", clusterName)
			continue
		}
		kubeconfigs[clusterName] = kubeconfig
	}

	return clusterSet, kubeconfigs, nil
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

// fetchKubeconfig retrieves the admin kubeconfig bytes for clusterName.
func fetchKubeconfig(ctx context.Context, c client.Client, clusterName string) ([]byte, error) {
	secret := &unstructured.Unstructured{}
	secret.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"})
	if err := c.Get(ctx, client.ObjectKey{
		Name:      clusterName + "-admin-kubeconfig",
		Namespace: clusterName,
	}, secret); err != nil {
		return nil, fmt.Errorf("getting kubeconfig secret for cluster %s: %w", clusterName, err)
	}

	data, _, _ := unstructured.NestedMap(secret.Object, "data")
	raw, ok := data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("'kubeconfig' key missing in secret for cluster %s", clusterName)
	}
	switch v := raw.(type) {
	case []byte:
		return v, nil
	case string:
		decoded, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("decoding kubeconfig for cluster %s: %w", clusterName, err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("unexpected kubeconfig type %T for cluster %s", raw, clusterName)
	}
}

// createAgenticRunOnCluster creates an AgenticRun in the openshift-lightspeed namespace
// on the target cluster identified by the provided kubeconfig.
func (h *Handler) createAgenticRunOnCluster(ctx context.Context, kubeconfig []byte, clusterName, alertName string) error {
	logger := log.FromContext(ctx)

	configMapName, ok := alertConfigMaps[alertName]
	if !ok {
		logger.Error(nil, "no AgenticRun ConfigMap configured for alert, skipping", "alertname", alertName)
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
	cfg = agenticrun.ExpandVariables(cfg, vars)

	spokeClient, err := h.NewSpokeClient(kubeconfig)
	if err != nil {
		return fmt.Errorf("building spoke client for cluster %s: %w", clusterName, err)
	}

	runName := fmt.Sprintf("telco-alert-%s-%d", strings.ToLower(alertName), time.Now().UnixNano())

	labels := map[string]string{
		"app.kubernetes.io/managed-by":     "telco-anomaly-detection",
		"telco-anomaly.io/trigger-type":    "alert",
		"telco-anomaly.io/trigger-alert":   alertName,
		"telco-anomaly.io/trigger-cluster": clusterName,
	}
	u, err := agenticrun.BuildObject(runName, labels, cfg)
	if err != nil {
		return fmt.Errorf("building AgenticRun object: %w", err)
	}

	if err := spokeClient.Create(ctx, u); err != nil {
		return fmt.Errorf("creating AgenticRun %s on cluster %s: %w", runName, clusterName, err)
	}

	logger.Info("created AgenticRun for alert",
		"cluster", clusterName,
		"alertname", alertName,
		"agenticRunName", runName)
	return nil
}
