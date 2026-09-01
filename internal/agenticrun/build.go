package agenticrun

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// BuildObject constructs an unstructured AgenticRun object ready for creation on a spoke cluster.
// The caller supplies the name and labels; the namespace is always Namespace.
// Fields absent or empty in cfg are omitted from the resulting spec.
func BuildObject(name string, labels map[string]string, cfg *RunConfig) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   Group,
		Version: Version,
		Kind:    Kind,
	})
	u.SetName(name)
	u.SetNamespace(Namespace)
	u.SetLabels(labels)

	if err := unstructured.SetNestedField(u.Object, cfg.Request, "spec", "request"); err != nil {
		return nil, fmt.Errorf("setting spec.request: %w", err)
	}

	if err := unstructured.SetNestedField(u.Object, "default", "spec", "analysis", "agent"); err != nil {
		return nil, fmt.Errorf("setting spec.analysis.agent: %w", err)
	}

	if err := unstructured.SetNestedField(u.Object, "default", "spec", "execution", "agent"); err != nil {
		return nil, fmt.Errorf("setting spec.execution.agent: %w", err)
	}

	// Build valid skills — the CRD requires paths to have at least 1 item per skill.
	var skills []interface{}
	for _, s := range cfg.Skills {
		if len(s.Paths) == 0 {
			continue
		}
		paths := make([]interface{}, len(s.Paths))
		for j, p := range s.Paths {
			paths[j] = p
		}
		skills = append(skills, map[string]interface{}{
			"image": s.Image,
			"paths": paths,
		})
	}

	if len(skills) > 0 {
		if err := unstructured.SetNestedSlice(u.Object, skills, "spec", "tools", "skills"); err != nil {
			return nil, fmt.Errorf("setting spec.tools.skills: %w", err)
		}
	}

	if len(cfg.MCPServers) > 0 {
		servers := make([]interface{}, len(cfg.MCPServers))
		for i, m := range cfg.MCPServers {
			entry := map[string]interface{}{
				"name": m.Name,
				"url":  m.URL,
			}
			if m.TimeoutSeconds != nil {
				entry["timeoutSeconds"] = int64(*m.TimeoutSeconds)
			}
			servers[i] = entry
		}
		if err := unstructured.SetNestedSlice(u.Object, servers, "spec", "tools", "mcpServers"); err != nil {
			return nil, fmt.Errorf("setting spec.tools.mcpServers: %w", err)
		}
	}

	return u, nil
}
