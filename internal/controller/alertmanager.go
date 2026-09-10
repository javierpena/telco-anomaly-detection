package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"gopkg.in/yaml.v3"
)

const (
	alertmanagerConfigSecret = "alertmanager-config"
	alertmanagerConfigKey    = "alertmanager.yaml"
	alertReceiverName        = "telco-anomaly-webhook"
)

// reconcileAlertManagerReceiver reads the ACM alertmanager-config Secret and ensures
// it includes a webhook receiver entry pointing to the alert receiver service URL.
// Any existing receiver with the same name is replaced; all other config is preserved.
func reconcileAlertManagerReceiver(ctx context.Context, c client.Client, alertReceiverURL string) error {
	logger := log.FromContext(ctx)
	logger.Info("reconciling AlertManager webhook receiver", "url", alertReceiverURL)

	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      alertmanagerConfigSecret,
		Namespace: observabilityNamespace,
	}, secret); err != nil {
		return fmt.Errorf("getting %s secret: %w", alertmanagerConfigSecret, err)
	}

	rawConfig := secret.Data[alertmanagerConfigKey]
	logger.V(1).Info("read alertmanager config", "bytes", len(rawConfig))

	updated, err := upsertWebhookReceiver(rawConfig, alertReceiverName, alertReceiverURL)
	if err != nil {
		return fmt.Errorf("updating alertmanager config: %w", err)
	}

	secret.Data[alertmanagerConfigKey] = updated
	if err := c.Update(ctx, secret); err != nil {
		return fmt.Errorf("updating %s secret: %w", alertmanagerConfigSecret, err)
	}

	logger.Info("updated AlertManager configuration with webhook receiver")
	return nil
}

// upsertWebhookReceiver merges a webhook receiver for receiverName and a matching
// continue-route into the raw alertmanager YAML config bytes.
func upsertWebhookReceiver(rawConfig []byte, receiverName, webhookURL string) ([]byte, error) {
	// Parse into a generic map to preserve unknown fields.
	var config map[string]interface{}
	if err := yaml.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("parsing alertmanager YAML: %w", err)
	}
	if config == nil {
		config = map[string]interface{}{}
	}

	// Build the new webhook receiver entry.
	webhookReceiver := map[string]interface{}{
		"name": receiverName,
		"webhook_configs": []interface{}{
			map[string]interface{}{
				"url":           webhookURL,
				"send_resolved": true,
			},
		},
	}

	// Upsert in the receivers list.
	config["receivers"] = upsertReceiver(config["receivers"], receiverName, webhookReceiver)

	// Ensure there is a continue-route pointing to this receiver.
	route, _ := config["route"].(map[string]interface{})
	if route == nil {
		route = map[string]interface{}{}
	}
	route["routes"] = upsertRoute(route["routes"], receiverName)
	config["route"] = route

	out, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshaling updated alertmanager config: %w", err)
	}
	return out, nil
}

// removeAlertManagerReceiver reads the ACM alertmanager-config Secret and removes the
// webhook receiver entry for alertReceiverName, along with its matching route entry.
// Returns nil if the Secret is not found (treat as already cleaned up).
func removeAlertManagerReceiver(ctx context.Context, c client.Client) error {
	logger := log.FromContext(ctx)

	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      alertmanagerConfigSecret,
		Namespace: observabilityNamespace,
	}, secret); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("alertmanager-config Secret not found, skipping cleanup")
			return nil
		}
		return fmt.Errorf("getting %s secret: %w", alertmanagerConfigSecret, err)
	}

	updated, err := deleteWebhookReceiver(secret.Data[alertmanagerConfigKey], alertReceiverName)
	if err != nil {
		return fmt.Errorf("removing receiver from alertmanager config: %w", err)
	}
	secret.Data[alertmanagerConfigKey] = updated
	if err := c.Update(ctx, secret); err != nil {
		return fmt.Errorf("updating %s secret: %w", alertmanagerConfigSecret, err)
	}
	logger.Info("removed AlertManager webhook receiver")
	return nil
}

// deleteWebhookReceiver removes receiverName from the receivers list and route.routes
// in the raw alertmanager YAML config bytes.
func deleteWebhookReceiver(rawConfig []byte, receiverName string) ([]byte, error) {
	var config map[string]interface{}
	if err := yaml.Unmarshal(rawConfig, &config); err != nil {
		return nil, fmt.Errorf("parsing alertmanager YAML: %w", err)
	}
	if config == nil {
		return rawConfig, nil
	}
	config["receivers"] = removeReceiver(config["receivers"], receiverName)
	if route, ok := config["route"].(map[string]interface{}); ok {
		route["routes"] = removeRoute(route["routes"], receiverName)
	}
	out, err := yaml.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshaling alertmanager config: %w", err)
	}
	return out, nil
}

// removeReceiver filters out the receiver matching receiverName from the list.
func removeReceiver(raw interface{}, receiverName string) []interface{} {
	receivers, _ := raw.([]interface{})
	out := make([]interface{}, 0, len(receivers))
	for _, r := range receivers {
		if m, ok := r.(map[string]interface{}); ok {
			if name, _ := m["name"].(string); name == receiverName {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// removeRoute filters out the route entry matching receiverName from the list.
func removeRoute(raw interface{}, receiverName string) []interface{} {
	routes, _ := raw.([]interface{})
	out := make([]interface{}, 0, len(routes))
	for _, r := range routes {
		if m, ok := r.(map[string]interface{}); ok {
			if recv, _ := m["receiver"].(string); recv == receiverName {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// upsertReceiver replaces the receiver matching receiverName or appends if not found.
func upsertReceiver(raw interface{}, receiverName string, entry map[string]interface{}) []interface{} {
	receivers, _ := raw.([]interface{})
	for i, r := range receivers {
		if m, ok := r.(map[string]interface{}); ok {
			if name, ok := m["name"].(string); ok && name == receiverName {
				receivers[i] = entry
				return receivers
			}
		}
	}
	return append(receivers, entry)
}

// upsertRoute adds or replaces a continue-route for receiverName.
// repeat_interval is set so AlertManager fires the webhook once on trigger and
// once on resolution (via send_resolved), but does not re-notify while the alert remains firing.
func upsertRoute(raw interface{}, receiverName string) []interface{} {
	routes, _ := raw.([]interface{})
	continueRoute := map[string]interface{}{
		"receiver":        receiverName,
		"continue":        true,
		"repeat_interval": "24h",
	}
	for i, r := range routes {
		if m, ok := r.(map[string]interface{}); ok {
			if recv, ok := m["receiver"].(string); ok && recv == receiverName {
				routes[i] = continueRoute
				return routes
			}
		}
	}
	return append(routes, continueRoute)
}
