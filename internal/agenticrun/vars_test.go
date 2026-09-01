package agenticrun

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newVarsTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	return s
}

func newVarsTestRESTMapper() apimeta.RESTMapper {
	m := apimeta.NewDefaultRESTMapper(nil)
	m.Add(routeGVK, apimeta.RESTScopeNamespace)
	return m
}

func makeRouteUnstructured(name, namespace, host string) *unstructured.Unstructured {
	r := &unstructured.Unstructured{}
	r.SetGroupVersionKind(routeGVK)
	r.SetName(name)
	r.SetNamespace(namespace)
	if host != "" {
		_ = unstructured.SetNestedSlice(r.Object, []interface{}{
			map[string]interface{}{"host": host},
		}, "status", "ingress")
	}
	return r
}

func buildFakeClient(t *testing.T, route *unstructured.Unstructured) *fake.ClientBuilder {
	t.Helper()
	b := fake.NewClientBuilder().
		WithScheme(newVarsTestScheme(t)).
		WithRESTMapper(newVarsTestRESTMapper())
	if route != nil {
		b = b.WithObjects(route)
	}
	return b
}

func TestBuildVarMap_AlwaysPresentVars(t *testing.T) {
	c := buildFakeClient(t, nil).Build()

	vars, err := BuildVarMap(context.Background(), c, "telco-healthcheck-system", "spoke-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["OPERATOR_NAMESPACE"] != "telco-healthcheck-system" {
		t.Errorf("unexpected OPERATOR_NAMESPACE: %q", vars["OPERATOR_NAMESPACE"])
	}
	if vars["CLUSTER_NAME"] != "spoke-1" {
		t.Errorf("unexpected CLUSTER_NAME: %q", vars["CLUSTER_NAME"])
	}
	if _, ok := vars["KUBE_COMPARE_MCP_URL"]; ok {
		t.Error("KUBE_COMPARE_MCP_URL should be absent when Route not found")
	}
}

func TestBuildVarMap_RouteFoundWithHost(t *testing.T) {
	route := makeRouteUnstructured(kubeCompareMCPRouteName, "telco-healthcheck-system", "mcp.apps.example.com")
	c := buildFakeClient(t, route).Build()

	vars, err := BuildVarMap(context.Background(), c, "telco-healthcheck-system", "spoke-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vars["KUBE_COMPARE_MCP_URL"] != "http://mcp.apps.example.com" {
		t.Errorf("unexpected KUBE_COMPARE_MCP_URL: %q", vars["KUBE_COMPARE_MCP_URL"])
	}
}

func TestBuildVarMap_RouteFoundNoIngress(t *testing.T) {
	route := makeRouteUnstructured(kubeCompareMCPRouteName, "telco-healthcheck-system", "")
	c := buildFakeClient(t, route).Build()

	vars, err := BuildVarMap(context.Background(), c, "telco-healthcheck-system", "spoke-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := vars["KUBE_COMPARE_MCP_URL"]; ok {
		t.Error("KUBE_COMPARE_MCP_URL should be absent when Route has no ingress")
	}
}

func TestBuildVarMap_RouteNotFound_NoError(t *testing.T) {
	c := buildFakeClient(t, nil).Build()

	_, err := BuildVarMap(context.Background(), c, "telco-healthcheck-system", "spoke-1")
	if err != nil {
		t.Errorf("expected nil error when Route not found, got: %v", err)
	}
}

func TestBuildVarMap_WrongNamespace_NotFound(t *testing.T) {
	route := makeRouteUnstructured(kubeCompareMCPRouteName, "other-namespace", "mcp.apps.example.com")
	c := buildFakeClient(t, route).Build()

	vars, err := BuildVarMap(context.Background(), c, "telco-healthcheck-system", "spoke-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := vars["KUBE_COMPARE_MCP_URL"]; ok {
		t.Error("KUBE_COMPARE_MCP_URL should be absent when Route is in the wrong namespace")
	}
}
