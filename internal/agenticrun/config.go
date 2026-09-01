package agenticrun

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RunConfig holds the AgenticRun configuration loaded from a ConfigMap.
// Fields left empty (absent key, empty string, or empty JSON array) are omitted
// from the resulting AgenticRun spec.
type RunConfig struct {
	Request    string
	Skills     []SkillSpec
	MCPServers []MCPServerSpec
}

// LoadRunConfig reads the named ConfigMap from namespace and parses the
// request, skills, and mcpServers data keys into a RunConfig.
// Missing or empty keys are silently skipped (not an error).
func LoadRunConfig(ctx context.Context, c client.Client, configMapName, namespace string) (*RunConfig, error) {
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: namespace}, cm); err != nil {
		return nil, fmt.Errorf("getting AgenticRun config ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	cfg := &RunConfig{}
	cfg.Request = cm.Data["request"]

	if raw := cm.Data["skills"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.Skills); err != nil {
			return nil, fmt.Errorf("parsing skills from ConfigMap %s/%s: %w", namespace, configMapName, err)
		}
	}

	if raw := cm.Data["mcpServers"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.MCPServers); err != nil {
			return nil, fmt.Errorf("parsing mcpServers from ConfigMap %s/%s: %w", namespace, configMapName, err)
		}
	}

	return cfg, nil
}
