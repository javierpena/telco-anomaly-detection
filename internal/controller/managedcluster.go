package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

// getMonitoredClusters returns the names of ManagedClusters to monitor based on the
// include/exclude lists in the spec. If Include is non-empty, only those clusters are
// returned. Otherwise, all clusters except those in Exclude are returned.
func getMonitoredClusters(ctx context.Context, c client.Client, spec ranv1alpha1.ManagedClustersSpec) ([]string, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("resolving monitored clusters", "include", spec.Include, "exclude", spec.Exclude)

	clusterList := &clusterv1.ManagedClusterList{}
	if err := c.List(ctx, clusterList); err != nil {
		return nil, fmt.Errorf("listing ManagedClusters: %w", err)
	}
	logger.V(1).Info("found ManagedClusters", "count", len(clusterList.Items))

	var monitored []string

	if len(spec.Include) > 0 {
		included := make(map[string]bool, len(spec.Include))
		for _, name := range spec.Include {
			included[name] = true
		}
		for _, cluster := range clusterList.Items {
			if included[cluster.Name] {
				logger.V(1).Info("including cluster", "cluster", cluster.Name)
				monitored = append(monitored, cluster.Name)
			} else {
				logger.V(1).Info("skipping cluster (not in include list)", "cluster", cluster.Name)
			}
		}
	} else {
		excluded := make(map[string]bool, len(spec.Exclude))
		for _, name := range spec.Exclude {
			excluded[name] = true
		}
		for _, cluster := range clusterList.Items {
			if excluded[cluster.Name] {
				logger.V(1).Info("excluding cluster", "cluster", cluster.Name)
			} else {
				logger.V(1).Info("monitoring cluster", "cluster", cluster.Name)
				monitored = append(monitored, cluster.Name)
			}
		}
	}

	logger.Info("resolved monitored clusters", "count", len(monitored))
	return monitored, nil
}

// getClusterKubeconfig retrieves the admin kubeconfig bytes for the named managed cluster.
// The kubeconfig is stored in a Secret named <clusterName>-admin-kubeconfig in the
// namespace that matches the cluster name.
func getClusterKubeconfig(ctx context.Context, c client.Client, clusterName string) ([]byte, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("fetching kubeconfig", "cluster", clusterName)

	secretKey := types.NamespacedName{
		Namespace: clusterName,
		Name:      clusterName + "-admin-kubeconfig",
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("getting kubeconfig secret %s/%s: %w",
			secretKey.Namespace, secretKey.Name, err)
	}

	kubeconfig, ok := secret.Data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("key 'kubeconfig' not found in secret %s/%s",
			secretKey.Namespace, secretKey.Name)
	}

	logger.V(1).Info("fetched kubeconfig", "cluster", clusterName, "bytes", len(kubeconfig))
	return kubeconfig, nil
}
