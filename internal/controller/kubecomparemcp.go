package controller

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed assets/kube-compare-rbac.yaml
var kubeCompareRBACYAML []byte

//go:embed assets/kube-compare-mcp.yaml
var kubeCompareMCPYAML []byte

const (
	kubeCompareRegistrySecretName = "kube-compare-registry-credentials"
	pullSecretName                = "pull-secret"
	pullSecretNamespace           = "openshift-config"
	kubeCompareFieldOwner         = "telco-anomaly-operator"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete;escalate;bind

// reconcileKubeCompareMCP creates or removes the kube-compare-mcp infrastructure
// based on whether RDS compliance checking is enabled in the spec.
func reconcileKubeCompareMCP(ctx context.Context, c client.Client, operatorNS string, enabled bool) error {
	if enabled {
		return ensureKubeCompareMCP(ctx, c, operatorNS)
	}
	return removeKubeCompareMCP(ctx, c, operatorNS)
}

// cleanupKubeCompareMCP removes all kube-compare-mcp resources unconditionally.
// Called during TelcoHealthcheck CR deletion.
func cleanupKubeCompareMCP(ctx context.Context, c client.Client, operatorNS string) error {
	return removeKubeCompareMCP(ctx, c, operatorNS)
}

func ensureKubeCompareMCP(ctx context.Context, c client.Client, operatorNS string) error {
	if err := ensureRegistryCredentials(ctx, c, operatorNS); err != nil {
		return err
	}
	for _, yamlData := range [][]byte{kubeCompareRBACYAML, kubeCompareMCPYAML} {
		if err := applyManifests(ctx, c, yamlData); err != nil {
			return err
		}
	}
	return nil
}

func removeKubeCompareMCP(ctx context.Context, c client.Client, operatorNS string) error {
	var firstErr error

	regCreds := &corev1.Secret{}
	regCreds.Name = kubeCompareRegistrySecretName
	regCreds.Namespace = operatorNS
	if err := client.IgnoreNotFound(c.Delete(ctx, regCreds)); err != nil {
		firstErr = fmt.Errorf("deleting registry credentials secret: %w", err)
	}

	// Delete MCP resources first (they depend on RBAC), then RBAC.
	for _, yamlData := range [][]byte{kubeCompareMCPYAML, kubeCompareRBACYAML} {
		if err := deleteManifests(ctx, c, yamlData); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func ensureRegistryCredentials(ctx context.Context, c client.Client, operatorNS string) error {
	pullSecret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: pullSecretName, Namespace: pullSecretNamespace}, pullSecret); err != nil {
		return fmt.Errorf("reading pull-secret from %s: %w", pullSecretNamespace, err)
	}

	existing := &corev1.Secret{}
	err := c.Get(ctx, types.NamespacedName{Name: kubeCompareRegistrySecretName, Namespace: operatorNS}, existing)
	if errors.IsNotFound(err) {
		if createErr := c.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kubeCompareRegistrySecretName,
				Namespace: operatorNS,
			},
			Type: pullSecret.Type,
			Data: pullSecret.Data,
		}); createErr != nil {
			return fmt.Errorf("creating registry credentials secret: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking registry credentials secret: %w", err)
	}
	existing.Data = pullSecret.Data
	existing.Type = pullSecret.Type
	if updateErr := c.Update(ctx, existing); updateErr != nil {
		return fmt.Errorf("updating registry credentials secret: %w", updateErr)
	}
	return nil
}

func applyManifests(ctx context.Context, c client.Client, data []byte) error {
	return forEachObject(data, func(obj *unstructured.Unstructured) error {
		return c.Patch(ctx, obj, client.Apply,
			client.ForceOwnership,
			client.FieldOwner(kubeCompareFieldOwner),
		)
	})
}

func deleteManifests(ctx context.Context, c client.Client, data []byte) error {
	return forEachObject(data, func(obj *unstructured.Unstructured) error {
		return client.IgnoreNotFound(c.Delete(ctx, obj))
	})
}

func forEachObject(data []byte, fn func(*unstructured.Unstructured) error) error {
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decoding manifest: %w", err)
		}
		if obj.Object == nil {
			continue
		}
		if err := fn(obj); err != nil {
			return fmt.Errorf("processing %s %q: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
}
