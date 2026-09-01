package agenticrun

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newConfigTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	return s
}

func makeRunConfigMap(name, namespace string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}
}

func TestLoadRunConfig_MissingConfigMap(t *testing.T) {
	scheme := newConfigTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := LoadRunConfig(context.Background(), c, "missing-cm", "test-ns")
	if err == nil {
		t.Error("expected error for missing ConfigMap, got nil")
	}
}

func TestLoadRunConfig_EmptyData(t *testing.T) {
	scheme := newConfigTestScheme(t)
	cm := makeRunConfigMap("my-config", "test-ns", map[string]string{})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	cfg, err := LoadRunConfig(context.Background(), c, "my-config", "test-ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Request != "" {
		t.Errorf("expected empty request, got %q", cfg.Request)
	}
	if len(cfg.Skills) != 0 {
		t.Errorf("expected no skills, got %d", len(cfg.Skills))
	}
	if len(cfg.MCPServers) != 0 {
		t.Errorf("expected no mcpServers, got %d", len(cfg.MCPServers))
	}
}

func TestLoadRunConfig_AllFields(t *testing.T) {
	scheme := newConfigTestScheme(t)
	cm := makeRunConfigMap("my-config", "test-ns", map[string]string{
		"request":    "diagnose network issue",
		"skills":     `[{"image":"quay.io/org/skills:v1","paths":["/skills/net.yaml"]}]`,
		"mcpServers": `[{"name":"my-mcp","url":"http://mcp.svc:8080"}]`,
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	cfg, err := LoadRunConfig(context.Background(), c, "my-config", "test-ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Request != "diagnose network issue" {
		t.Errorf("unexpected request: %q", cfg.Request)
	}
	if len(cfg.Skills) != 1 || cfg.Skills[0].Image != "quay.io/org/skills:v1" {
		t.Errorf("unexpected skills: %+v", cfg.Skills)
	}
	if len(cfg.Skills[0].Paths) != 1 || cfg.Skills[0].Paths[0] != "/skills/net.yaml" {
		t.Errorf("unexpected skill paths: %+v", cfg.Skills[0].Paths)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != "my-mcp" || cfg.MCPServers[0].URL != "http://mcp.svc:8080" {
		t.Errorf("unexpected mcpServers: %+v", cfg.MCPServers)
	}
}

func TestLoadRunConfig_EmptyJSONArrays(t *testing.T) {
	scheme := newConfigTestScheme(t)
	cm := makeRunConfigMap("my-config", "test-ns", map[string]string{
		"request":    "test",
		"skills":     "[]",
		"mcpServers": "[]",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	cfg, err := LoadRunConfig(context.Background(), c, "my-config", "test-ns")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Skills) != 0 {
		t.Errorf("expected no skills from empty array, got %d", len(cfg.Skills))
	}
	if len(cfg.MCPServers) != 0 {
		t.Errorf("expected no mcpServers from empty array, got %d", len(cfg.MCPServers))
	}
}

func TestLoadRunConfig_InvalidSkillsJSON(t *testing.T) {
	scheme := newConfigTestScheme(t)
	cm := makeRunConfigMap("my-config", "test-ns", map[string]string{
		"skills": "not-json",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	_, err := LoadRunConfig(context.Background(), c, "my-config", "test-ns")
	if err == nil {
		t.Error("expected error for invalid skills JSON, got nil")
	}
}

func TestLoadRunConfig_InvalidMCPServersJSON(t *testing.T) {
	scheme := newConfigTestScheme(t)
	cm := makeRunConfigMap("my-config", "test-ns", map[string]string{
		"mcpServers": "{bad json}",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	_, err := LoadRunConfig(context.Background(), c, "my-config", "test-ns")
	if err == nil {
		t.Error("expected error for invalid mcpServers JSON, got nil")
	}
}
