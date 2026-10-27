package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	observabilityMetricsConfigMap = "observability-metrics-custom-allowlist"
	metricsListKey                = "metrics_list.yaml"
	userWorkloadMetricsListKey    = "uwl_metrics_list.yaml"
)

// reconcileObservabilityMetrics creates or updates the observability-metrics-custom-allowlist
// ConfigMap in the open-cluster-management-observability namespace. It lists system-alert and
// (when enabled) user-alert ConfigMaps in namespace and builds separate metric lists from
// their alertMetrics and alertUserWorkloadMetrics fields.
func reconcileObservabilityMetrics(ctx context.Context, c client.Client, namespace string, userAlertsEnabled bool) error {
	logger := log.FromContext(ctx)
	logger.Info("reconciling observability metrics allowlist",
		"namespace", namespace,
		"userAlertsEnabled", userAlertsEnabled)

	allAlerts, err := listAllAlertConfigs(ctx, c, namespace, userAlertsEnabled)
	if err != nil {
		return fmt.Errorf("listing alert configs for metrics: %w", err)
	}

	metricsContent, userWorkloadMetricsContent := buildMetricsListYAML(allAlerts)
	logger.V(1).Info("built metrics_list.yaml", "content", metricsContent)
	logger.V(1).Info("built uwl_metrics_list.yaml", "content", userWorkloadMetricsContent)

	existing := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: observabilityMetricsConfigMap, Namespace: observabilityNamespace}
	err = c.Get(ctx, key, existing)

	if errors.IsNotFound(err) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      observabilityMetricsConfigMap,
				Namespace: observabilityNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "telco-anomaly-detection",
				},
			},
			Data: map[string]string{
				metricsListKey:             metricsContent,
				userWorkloadMetricsListKey: userWorkloadMetricsContent,
			},
		}
		logger.Info("creating observability-metrics-custom-allowlist ConfigMap")
		if err := c.Create(ctx, cm); err != nil {
			return fmt.Errorf("creating %s ConfigMap: %w", observabilityMetricsConfigMap, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting %s ConfigMap: %w", observabilityMetricsConfigMap, err)
	}

	if existing.Data == nil {
		existing.Data = make(map[string]string)
	}
	existing.Data[metricsListKey] = metricsContent
	existing.Data[userWorkloadMetricsListKey] = userWorkloadMetricsContent
	logger.Info("updating observability-metrics-custom-allowlist ConfigMap")
	if err := c.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating %s ConfigMap: %w", observabilityMetricsConfigMap, err)
	}
	return nil
}

// cleanupObservabilityMetrics deletes the observability-metrics-custom-allowlist ConfigMap.
// Returns nil if the ConfigMap is not found (treat as already cleaned up).
func cleanupObservabilityMetrics(ctx context.Context, c client.Client) error {
	logger := log.FromContext(ctx)

	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      observabilityMetricsConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("observability-metrics-custom-allowlist ConfigMap not found, skipping cleanup")
			return nil
		}
		return fmt.Errorf("getting %s ConfigMap: %w", observabilityMetricsConfigMap, err)
	}

	if err := c.Delete(ctx, cm); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting %s ConfigMap: %w", observabilityMetricsConfigMap, err)
	}
	logger.Info("deleted observability-metrics-custom-allowlist ConfigMap")
	return nil
}

// buildMetricsListYAML produces the metrics_list.yaml and uwl_metrics_list.yaml contents
// for the MCO custom allowlist. Metric names are deduplicated independently in each list.
func buildMetricsListYAML(allAlerts []UserAlertConfig) (string, string) {
	var metrics, userWorkloadMetrics []string
	for _, a := range allAlerts {
		metrics = append(metrics, a.AlertMetrics...)
		userWorkloadMetrics = append(userWorkloadMetrics, a.AlertUserWorkloadMetrics...)
	}
	return buildMetricNamesYAML(metrics), buildMetricNamesYAML(userWorkloadMetrics)
}

// buildMetricNamesYAML renders a deduplicated metric list in first-seen order.
func buildMetricNamesYAML(metrics []string) string {
	seen := make(map[string]bool)
	var names []string

	for _, m := range metrics {
		if !seen[m] {
			seen[m] = true
			names = append(names, m)
		}
	}

	if len(names) == 0 {
		return "names: []\n"
	}

	content := "names:\n"
	for _, n := range names {
		content += "  - " + n + "\n"
	}
	return content
}
