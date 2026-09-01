package agenticrun

import (
	"testing"
)

func ptr[T any](v T) *T { return &v }

func TestExpandVariables_AllFields(t *testing.T) {
	cfg := &RunConfig{
		Request: "check ${CLUSTER_NAME} in ${OPERATOR_NAMESPACE}",
		Skills: []SkillSpec{
			{Image: "quay.io/${ORG}/skills:v1", Paths: []string{"/skills/net.yaml"}},
		},
		MCPServers: []MCPServerSpec{
			{Name: "${MCP_NAME}", URL: "${MCP_URL}", TimeoutSeconds: ptr(int32(10))},
		},
	}
	vars := map[string]string{
		"CLUSTER_NAME":       "spoke-1",
		"OPERATOR_NAMESPACE": "telco-healthcheck-system",
		"ORG":                "myorg",
		"MCP_NAME":           "kube-compare-mcp",
		"MCP_URL":            "https://mcp.apps.example.com",
	}

	out := ExpandVariables(cfg, vars)

	if out.Request != "check spoke-1 in telco-healthcheck-system" {
		t.Errorf("unexpected Request: %q", out.Request)
	}
	if len(out.Skills) != 1 || out.Skills[0].Image != "quay.io/myorg/skills:v1" {
		t.Errorf("unexpected Skills: %+v", out.Skills)
	}
	if out.Skills[0].Paths[0] != "/skills/net.yaml" {
		t.Errorf("Paths should be copied verbatim, got %v", out.Skills[0].Paths)
	}
	if len(out.MCPServers) != 1 {
		t.Fatalf("expected 1 MCPServer, got %d", len(out.MCPServers))
	}
	if out.MCPServers[0].Name != "kube-compare-mcp" {
		t.Errorf("unexpected MCPServer.Name: %q", out.MCPServers[0].Name)
	}
	if out.MCPServers[0].URL != "https://mcp.apps.example.com" {
		t.Errorf("unexpected MCPServer.URL: %q", out.MCPServers[0].URL)
	}
	if out.MCPServers[0].TimeoutSeconds == nil || *out.MCPServers[0].TimeoutSeconds != 10 {
		t.Errorf("TimeoutSeconds should be copied verbatim")
	}
}

func TestExpandVariables_UnknownVarsPreserved(t *testing.T) {
	cfg := &RunConfig{
		Request: "url is ${KUBE_COMPARE_MCP_URL}",
	}

	out := ExpandVariables(cfg, map[string]string{})

	if out.Request != "url is ${KUBE_COMPARE_MCP_URL}" {
		t.Errorf("unknown var should be preserved, got %q", out.Request)
	}
}

func TestExpandVariables_EmptyVarMap(t *testing.T) {
	cfg := &RunConfig{
		Request: "static request",
		MCPServers: []MCPServerSpec{
			{Name: "mcp", URL: "http://mcp.svc:8080"},
		},
	}

	out := ExpandVariables(cfg, map[string]string{})

	if out.Request != "static request" {
		t.Errorf("unexpected Request: %q", out.Request)
	}
	if out.MCPServers[0].URL != "http://mcp.svc:8080" {
		t.Errorf("unexpected URL: %q", out.MCPServers[0].URL)
	}
}

func TestExpandVariables_NilSlices(t *testing.T) {
	cfg := &RunConfig{Request: "req"}

	out := ExpandVariables(cfg, map[string]string{"X": "y"})

	if out.Skills != nil {
		t.Errorf("expected nil Skills, got %v", out.Skills)
	}
	if out.MCPServers != nil {
		t.Errorf("expected nil MCPServers, got %v", out.MCPServers)
	}
}

func TestExpandVariables_DoesNotMutateInput(t *testing.T) {
	cfg := &RunConfig{
		Request: "check ${CLUSTER_NAME}",
	}
	vars := map[string]string{"CLUSTER_NAME": "spoke-1"}

	_ = ExpandVariables(cfg, vars)

	if cfg.Request != "check ${CLUSTER_NAME}" {
		t.Errorf("input cfg was mutated: %q", cfg.Request)
	}
}
