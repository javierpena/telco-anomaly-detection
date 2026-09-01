package agenticrun

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildObject_FullConfig(t *testing.T) {
	cfg := &RunConfig{
		Request: "test-request",
		Skills: []SkillSpec{
			{Image: "quay.io/test:latest", Paths: []string{"/skills/test.yaml"}},
		},
		MCPServers: []MCPServerSpec{
			{Name: "test-mcp", URL: "http://mcp.example.com"},
		},
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by":     "telco-anomaly-detection",
		"telco-anomaly.io/healthcheck-ref": "my-healthcheck",
	}

	obj, err := BuildObject("test-run", labels, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if obj.GetName() != "test-run" {
		t.Errorf("expected name 'test-run', got %q", obj.GetName())
	}
	if obj.GetNamespace() != Namespace {
		t.Errorf("expected namespace %q, got %q", Namespace, obj.GetNamespace())
	}
	gvk := obj.GroupVersionKind()
	if gvk.Group != Group || gvk.Version != Version || gvk.Kind != Kind {
		t.Errorf("unexpected GVK: %v", gvk)
	}

	req, found, err := unstructured.NestedString(obj.Object, "spec", "request")
	if err != nil || !found || req != cfg.Request {
		t.Errorf("unexpected spec.request: found=%v err=%v value=%q", found, err, req)
	}

	agent, found, err := unstructured.NestedString(obj.Object, "spec", "analysis", "agent")
	if err != nil || !found || agent != "default" {
		t.Errorf("unexpected spec.analysis.agent: found=%v err=%v value=%q", found, err, agent)
	}

	execAgent, found, err := unstructured.NestedString(obj.Object, "spec", "execution", "agent")
	if err != nil || !found || execAgent != "default" {
		t.Errorf("unexpected spec.execution.agent: found=%v err=%v value=%q", found, err, execAgent)
	}

	skills, found, err := unstructured.NestedSlice(obj.Object, "spec", "tools", "skills")
	if err != nil || !found || len(skills) != 1 {
		t.Errorf("unexpected spec.tools.skills: found=%v err=%v len=%d", found, err, len(skills))
	}
	if len(skills) > 0 {
		if skillMap, ok := skills[0].(map[string]interface{}); ok {
			if skillMap["image"] != cfg.Skills[0].Image {
				t.Errorf("unexpected skills image: %v", skillMap["image"])
			}
		}
	}

	if obj.GetLabels()["telco-anomaly.io/healthcheck-ref"] != "my-healthcheck" {
		t.Errorf("unexpected healthcheck-ref label: %q", obj.GetLabels()["telco-anomaly.io/healthcheck-ref"])
	}
}

func TestBuildObject_EmptyToolsOmitted(t *testing.T) {
	cfg := &RunConfig{Request: "minimal-request"}

	obj, err := BuildObject("test-run", nil, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agent, found, err := unstructured.NestedString(obj.Object, "spec", "analysis", "agent")
	if err != nil || !found || agent != "default" {
		t.Errorf("unexpected spec.analysis.agent: found=%v err=%v value=%q", found, err, agent)
	}

	execAgent, found, err := unstructured.NestedString(obj.Object, "spec", "execution", "agent")
	if err != nil || !found || execAgent != "default" {
		t.Errorf("unexpected spec.execution.agent: found=%v err=%v value=%q", found, err, execAgent)
	}

	_, foundSkills, _ := unstructured.NestedSlice(obj.Object, "spec", "tools", "skills")
	if foundSkills {
		t.Error("expected spec.tools.skills to be absent when config has no skills")
	}
	_, foundMCP, _ := unstructured.NestedSlice(obj.Object, "spec", "tools", "mcpServers")
	if foundMCP {
		t.Error("expected spec.tools.mcpServers to be absent when config has no mcpServers")
	}
}

func TestBuildObject_SkillsWithoutMCPServers(t *testing.T) {
	cfg := &RunConfig{
		Request: "test-request",
		Skills:  []SkillSpec{{Image: "quay.io/test:latest", Paths: []string{"/skills/test.yaml"}}},
	}

	obj, err := BuildObject("test-run", nil, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, foundSkills, _ := unstructured.NestedSlice(obj.Object, "spec", "tools", "skills")
	if !foundSkills {
		t.Error("expected spec.tools.skills to be present when skills are configured")
	}
	_, foundMCP, _ := unstructured.NestedSlice(obj.Object, "spec", "tools", "mcpServers")
	if foundMCP {
		t.Error("expected spec.tools.mcpServers to be absent when mcpServers is empty")
	}
}

func TestBuildObject_MCPServersWithoutSkills(t *testing.T) {
	cfg := &RunConfig{
		Request:    "test-request",
		MCPServers: []MCPServerSpec{{Name: "kube-compare-mcp", URL: "http://mcp.example.com"}},
	}

	obj, err := BuildObject("test-run", nil, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, foundSkills, _ := unstructured.NestedSlice(obj.Object, "spec", "tools", "skills")
	if foundSkills {
		t.Error("expected spec.tools.skills to be absent when no skills are configured")
	}
	_, foundMCP, _ := unstructured.NestedSlice(obj.Object, "spec", "tools", "mcpServers")
	if !foundMCP {
		t.Error("expected spec.tools.mcpServers to be present when mcpServers are configured")
	}
}

func TestBuildObject_EmptyPathsFiltered(t *testing.T) {
	cfg := &RunConfig{
		Request: "test-request",
		Skills:  []SkillSpec{{Image: "quay.io/test:latest", Paths: []string{}}},
		MCPServers: []MCPServerSpec{
			{Name: "test-mcp", URL: "http://mcp.example.com"},
		},
	}

	obj, err := BuildObject("test-run", nil, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, foundSkills, _ := unstructured.NestedSlice(obj.Object, "spec", "tools", "skills")
	if foundSkills {
		t.Error("expected spec.tools.skills to be absent when all skills have empty paths")
	}
}
