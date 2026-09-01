package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// agenticRunConfigMapNames lists all AgenticRun configuration ConfigMaps managed by the operator.
var agenticRunConfigMapNames = []string{
	"telco-anomaly-host-network-config",
	"telco-anomaly-pod-network-config",
	"telco-anomaly-rds-compliance-config",
}

// ensureAgenticRunConfigs creates the AgenticRun configuration ConfigMaps in namespace if they
// do not already exist. Existing ConfigMaps are left unchanged so operators can customise them.
func ensureAgenticRunConfigs(ctx context.Context, c client.Client, namespace string) error {
	for _, name := range agenticRunConfigMapNames {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "telco-anomaly-detection",
				},
			},
			Data: map[string]string{
				"request":    "test",
				"skills":     "[]",
				"mcpServers": "[]",
			},
		}
		if err := c.Create(ctx, cm); client.IgnoreAlreadyExists(err) != nil {
			return fmt.Errorf("creating AgenticRun config ConfigMap %s: %w", name, err)
		}
	}
	return nil
}
