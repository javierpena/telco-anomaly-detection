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

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

const (
	observabilityNamespace = "open-cluster-management-observability"
	thanosRulerConfigMap   = "thanos-ruler-custom-rules"
	customRulesKey         = "custom_rules.yaml"
)

// reconcileAlertRules creates or updates the thanos-ruler-custom-rules ConfigMap in
// the open-cluster-management-observability namespace. The ConfigMap content is driven
// by the alerts field in the TelcoHealthcheck spec; actual rule definitions are added
// in a later phase. The thanos-ruler config-reload sidecar picks up changes automatically.
func reconcileAlertRules(ctx context.Context, c client.Client, spec ranv1alpha1.TelcoHealthcheckSpec) error {
	logger := log.FromContext(ctx)
	logger.Info("reconciling Thanos custom alert rules",
		"hostNetwork", spec.Alerts.HostNetwork,
		"podNetwork", spec.Alerts.PodNetwork,
		"hostReservedCPU", spec.Alerts.HostReservedCPU,
		"ovsProcessCPU", spec.Alerts.OVSProcessCPU)

	rulesContent := buildCustomRulesYAML(spec.Alerts)
	logger.V(1).Info("built custom_rules.yaml", "content", rulesContent)

	existing := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: thanosRulerConfigMap, Namespace: observabilityNamespace}
	err := c.Get(ctx, key, existing)

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

// buildCustomRulesYAML produces the Prometheus rules YAML for the Thanos ConfigMap.
// Rule groups are included or omitted based on the alerts spec.
func buildCustomRulesYAML(alerts ranv1alpha1.AlertsSpec) string {
	if !alerts.HostNetwork && !alerts.PodNetwork && !alerts.HostReservedCPU && !alerts.OVSProcessCPU {
		return "groups: []\n"
	}

	content := "groups:\n"
	if alerts.HostNetwork {
		content += hostNetworkRuleGroup()
	}
	if alerts.PodNetwork {
		content += podNetworkRuleGroup()
	}
	if alerts.HostReservedCPU {
		content += hostReservedCPURuleGroup()
	}
	if alerts.OVSProcessCPU {
		content += ovsProcessCPURuleGroup()
	}
	return content
}

func hostNetworkRuleGroup() string {
	return `  - name: telco-host-network
    rules:
      - alert: TelcoHealthCheckHostNetwork
        expr: sum by (clusterID, cluster, instance, prometheus) (instance:node_network_receive_drop_excluding_lo:rate1m > 1) or sum by (clusterID, cluster, instance, prometheus) (instance:node_network_transmit_drop_excluding_lo:rate1m > 1)
        for: 1m
        labels:
          severity: warning
        annotations:
          cluster: '{{ $labels.cluster }}'
          node: '{{ $labels.instance }}'
`
}

func podNetworkRuleGroup() string {
	return `  - name: telco-pod-network
    rules:
      - alert: TelcoHealthCheckPodNetwork
        expr: sum by (clusterID, cluster, instance, pod) (container_network_receive_errors_total > 1) or sum by (clusterID, cluster, instance, pod) (container_network_receive_packets_dropped_total > 1) or sum by (clusterID, cluster, instance, pod) (container_network_transmit_errors_total > 1) or sum by (clusterID, cluster, instance, pod) (container_network_transmit_packets_dropped_total > 1)
        for: 1m
        labels:
          severity: warning
        annotations:
          cluster: '{{ $labels.cluster }}'
          node: '{{ $labels.instance }}'
          pod: '{{ $labels.pod }}'
          interface: '{{ $labels.interface }}'
          namespace: '{{ $labels.namespace }}'
`
}

func hostReservedCPURuleGroup() string {
	return `  - name: telco-host-reserved-cpu
    rules:
      - alert: TelcoHealthCheckHostReservedCPU
        expr: "openshift:cpu_usage_cores:sum > 3"
        for: 1m
        labels:
          severity: warning
        annotations:
          cluster: '{{ $labels.cluster }}'
`
}

func ovsProcessCPURuleGroup() string {
	return `  - name: telco-ovs-process-cpu
    rules:
      - alert: TelcoHealthCheckOVSProcessCPU
        expr: irate(ovs_db_process_cpu_seconds_total[10m]) > 1.0 or irate(ovs_vswitchd_process_cpu_seconds_total[10m]) > 1.0
        for: 1m
        labels:
          severity: warning
        annotations:
          cluster: '{{ $labels.cluster }}'
          node: '{{ $labels.instance }}'
`
}

// parseAlertNamesFromRulesYAML extracts alert names from a Prometheus rules YAML string.
// Returns an empty map (not an error) when there are no groups or rules.
func parseAlertNamesFromRulesYAML(yaml string) (map[string]bool, error) {
	// Lightweight parsing: look for "alert: <name>" lines.
	// A full YAML parse is avoided to keep this dependency-free.
	// Once real rules are added this can be upgraded to a proper YAML parse.
	names := make(map[string]bool)
	parseAlertNamesFromString(yaml, names)
	return names, nil
}

// parseAlertNamesFromString scans lines for "  - alert: <name>" patterns.
func parseAlertNamesFromString(input string, out map[string]bool) {
	i := 0
	for i < len(input) {
		// find next newline
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

// extractAlertName returns the alert name if the line matches "  - alert: <name>".
func extractAlertName(line string) string {
	const prefix = "alert:"
	for k := 0; k < len(line); k++ {
		if k+len(prefix) <= len(line) && line[k:k+len(prefix)] == prefix {
			rest := line[k+len(prefix):]
			// trim leading spaces
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
