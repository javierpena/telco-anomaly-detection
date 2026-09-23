package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	observabilityNamespace = "open-cluster-management-observability"
	thanosRulerConfigMap   = "thanos-ruler-custom-rules"
	customRulesKey         = "custom_rules.yaml"
)

// reconcileAlertRules creates or updates the thanos-ruler-custom-rules ConfigMap in
// the open-cluster-management-observability namespace. It lists system-alert ConfigMaps
// (always) and user-alert ConfigMaps (when userAlertsEnabled) in namespace, then builds
// the rules YAML from the combined set.
func reconcileAlertRules(ctx context.Context, c client.Client, namespace string, userAlertsEnabled bool) error {
	logger := log.FromContext(ctx)
	logger.Info("reconciling Thanos custom alert rules",
		"namespace", namespace,
		"userAlertsEnabled", userAlertsEnabled)

	allAlerts, err := listAllAlertConfigs(ctx, c, namespace, userAlertsEnabled)
	if err != nil {
		return fmt.Errorf("listing alert configs: %w", err)
	}

	rulesContent := buildCustomRulesYAML(allAlerts)
	logger.V(1).Info("built custom_rules.yaml", "content", rulesContent)

	existing := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: thanosRulerConfigMap, Namespace: observabilityNamespace}
	err = c.Get(ctx, key, existing)

	if errors.IsNotFound(err) {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      thanosRulerConfigMap,
				Namespace: observabilityNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "telco-anomaly-detection",
				},
			},
			Data: map[string]string{customRulesKey: rulesContent},
		}
		logger.Info("creating thanos-ruler-custom-rules ConfigMap")
		if err := c.Create(ctx, cm); err != nil {
			return fmt.Errorf("creating %s ConfigMap: %w", thanosRulerConfigMap, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting %s ConfigMap: %w", thanosRulerConfigMap, err)
	}

	existing.Data[customRulesKey] = rulesContent
	logger.Info("updating thanos-ruler-custom-rules ConfigMap")
	if err := c.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating %s ConfigMap: %w", thanosRulerConfigMap, err)
	}
	return nil
}

// cleanupAlertRules deletes the thanos-ruler-custom-rules ConfigMap from the observability
// namespace. Returns nil if the ConfigMap is not found (treat as already cleaned up).
func cleanupAlertRules(ctx context.Context, c client.Client) error {
	logger := log.FromContext(ctx)

	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      thanosRulerConfigMap,
		Namespace: observabilityNamespace,
	}, cm); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("thanos-ruler-custom-rules ConfigMap not found, skipping cleanup")
			return nil
		}
		return fmt.Errorf("getting %s ConfigMap: %w", thanosRulerConfigMap, err)
	}

	if err := c.Delete(ctx, cm); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting %s ConfigMap: %w", thanosRulerConfigMap, err)
	}
	logger.Info("deleted thanos-ruler-custom-rules ConfigMap")
	return nil
}

// listAllAlertConfigs returns parsed configs from system-alert ConfigMaps (always) and
// user-alert ConfigMaps (when userAlertsEnabled) in namespace.
func listAllAlertConfigs(ctx context.Context, c client.Client, namespace string, userAlertsEnabled bool) ([]UserAlertConfig, error) {
	logger := log.FromContext(ctx)
	var all []UserAlertConfig

	var sysCMs corev1.ConfigMapList
	if err := c.List(ctx, &sysCMs,
		client.InNamespace(namespace),
		client.MatchingLabels{
			userAlertManagedByLabel: userAlertManagedByValue,
			systemAlertLabel:        systemAlertLabelValue,
		},
	); err != nil {
		return nil, fmt.Errorf("listing system alert ConfigMaps: %w", err)
	}
	for _, cm := range sysCMs.Items {
		cfg, ok := parseAlertConfigMap(cm)
		if !ok {
			logger.Info("system-alert ConfigMap missing alertName or alertRule, skipping", "configmap", cm.Name)
			continue
		}
		all = append(all, cfg)
	}

	if userAlertsEnabled {
		var userCMs corev1.ConfigMapList
		if err := c.List(ctx, &userCMs,
			client.InNamespace(namespace),
			client.MatchingLabels{
				userAlertManagedByLabel: userAlertManagedByValue,
				userAlertLabel:          userAlertLabelValue,
			},
		); err != nil {
			return nil, fmt.Errorf("listing user alert ConfigMaps: %w", err)
		}
		for _, cm := range userCMs.Items {
			cfg, ok := parseAlertConfigMap(cm)
			if !ok {
				logger.Info("user-alert ConfigMap missing alertName or alertRule, skipping", "configmap", cm.Name)
				continue
			}
			all = append(all, cfg)
		}
	}

	return all, nil
}

// buildCustomRulesYAML produces the Prometheus rules YAML for the Thanos ConfigMap.
// For each alert, GroupName is used when set; otherwise defaults to "telco-user-<alertname-lowercased>".
func buildCustomRulesYAML(allAlerts []UserAlertConfig) string {
	if len(allAlerts) == 0 {
		return "groups: []\n"
	}

	content := "groups:\n"
	for _, a := range allAlerts {
		groupName := a.GroupName
		if groupName == "" {
			groupName = "telco-user-" + strings.ToLower(a.AlertName)
		}
		indented := strings.ReplaceAll(strings.TrimSpace(a.AlertRule), "\n", "\n    ")
		content += fmt.Sprintf("  - name: %s\n    rules:\n    %s\n", groupName, indented)
	}
	return content
}

// parseAlertNamesFromRulesYAML extracts alert names from a Prometheus rules YAML string.
// Returns an empty map (not an error) when there are no groups or rules.
func parseAlertNamesFromRulesYAML(yaml string) (map[string]bool, error) {
	names := make(map[string]bool)
	parseAlertNamesFromString(yaml, names)
	return names, nil
}

// parseAlertNamesFromString scans lines for "alert: <name>" patterns.
func parseAlertNamesFromString(input string, out map[string]bool) {
	i := 0
	for i < len(input) {
		j := i
		for j < len(input) && input[j] != '\n' {
			j++
		}
		line := input[i:j]
		if name := extractAlertName(line); name != "" {
			out[name] = true
		}
		i = j + 1
	}
}

// extractAlertName returns the alert name if the line contains "alert: <name>".
func extractAlertName(line string) string {
	const prefix = "alert:"
	for k := 0; k < len(line); k++ {
		if k+len(prefix) <= len(line) && line[k:k+len(prefix)] == prefix {
			rest := line[k+len(prefix):]
			start := 0
			for start < len(rest) && (rest[start] == ' ' || rest[start] == '\t') {
				start++
			}
			name := rest[start:]
			if len(name) > 0 {
				return name
			}
		}
	}
	return ""
}
