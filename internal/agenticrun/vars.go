package agenticrun

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var routeGVK = schema.GroupVersionKind{
	Group:   "route.openshift.io",
	Version: "v1",
	Kind:    "Route",
}

const kubeCompareMCPRouteName = "telco-anomaly-kube-compare-mcp"

// BuildVarMap resolves the standard set of template variables for AgenticRun ConfigMap expansion.
//
// Variables always populated:
//   - OPERATOR_NAMESPACE
//   - CLUSTER_NAME
//
// Variables conditionally populated (omitted when unavailable, leaving placeholders unexpanded):
//   - KUBE_COMPARE_MCP_URL — HTTP URL of the kube-compare-mcp Route; absent when the
//     Route does not exist yet or has no ingress host assigned.
func BuildVarMap(
	ctx context.Context,
	hubClient client.Client,
	operatorNamespace, clusterName string,
) (map[string]string, error) {
	logger := log.FromContext(ctx)

	vars := map[string]string{
		"OPERATOR_NAMESPACE": operatorNamespace,
		"CLUSTER_NAME":       clusterName,
	}

	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(routeGVK)
	err := hubClient.Get(ctx, client.ObjectKey{
		Name:      kubeCompareMCPRouteName,
		Namespace: operatorNamespace,
	}, route)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("kube-compare-mcp Route not found; KUBE_COMPARE_MCP_URL will not be expanded",
				"route", kubeCompareMCPRouteName, "namespace", operatorNamespace)
			return vars, nil
		}
		return nil, fmt.Errorf("getting Route %s/%s: %w", operatorNamespace, kubeCompareMCPRouteName, err)
	}

	ingress, _, _ := unstructured.NestedSlice(route.Object, "status", "ingress")
	if len(ingress) == 0 {
		logger.V(1).Info("kube-compare-mcp Route has no ingress entries yet; KUBE_COMPARE_MCP_URL will not be expanded")
		return vars, nil
	}

	ingressEntry, ok := ingress[0].(map[string]interface{})
	if !ok {
		logger.V(1).Info("kube-compare-mcp Route ingress[0] has unexpected type; KUBE_COMPARE_MCP_URL will not be expanded")
		return vars, nil
	}

	host, _, _ := unstructured.NestedString(ingressEntry, "host")
	if host == "" {
		logger.V(1).Info("kube-compare-mcp Route ingress[0].host is empty; KUBE_COMPARE_MCP_URL will not be expanded")
		return vars, nil
	}

	vars["KUBE_COMPARE_MCP_URL"] = "http://" + host
	return vars, nil
}
