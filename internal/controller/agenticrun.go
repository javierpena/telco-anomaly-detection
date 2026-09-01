package controller

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
)

const (
	operatorNamespace = "telco-healthcheck-system"
)

// checkTypeConfigMaps maps a periodic check type to the ConfigMap that holds its AgenticRun config.
var checkTypeConfigMaps = map[string]string{
	"rds-compliance": "telco-anomaly-rds-compliance-config",
}

// createAgenticRunsForClusters creates an AgenticRun on each listed managed cluster.
// Clusters that fail (kubeconfig fetch or AgenticRun creation) are logged and skipped;
// errors are non-fatal to allow the others to proceed.
func createAgenticRunsForClusters(
	ctx context.Context,
	c client.Client,
	thc *ranv1alpha1.TelcoHealthcheck,
	monitoredClusters []string,
	checkType string,
) error {
	logger := log.FromContext(ctx)

	configMapName, ok := checkTypeConfigMaps[checkType]
	if !ok {
		return fmt.Errorf("no AgenticRun ConfigMap configured for check type %q", checkType)
	}

	cfg, err := agenticrun.LoadRunConfig(ctx, c, configMapName, operatorNamespace)
	if err != nil {
		return fmt.Errorf("loading AgenticRun config for check type %q: %w", checkType, err)
	}
	if cfg.Request == "" {
		return fmt.Errorf("AgenticRun config for check type %q has empty request field", checkType)
	}

	logger.Info("creating AgenticRun resources", "checkType", checkType, "clusterCount", len(monitoredClusters))

	runName := fmt.Sprintf("telco-health-%s-%d", checkType, time.Now().UnixNano())

	for _, clusterName := range monitoredClusters {
		logger.V(1).Info("processing cluster", "cluster", clusterName, "checkType", checkType)

		kubeconfig, err := getClusterKubeconfig(ctx, c, clusterName)
		if err != nil {
			logger.Error(err, "failed to get kubeconfig, skipping cluster", "cluster", clusterName)
			continue
		}

		spokeClient, err := buildSpokeClient(kubeconfig)
		if err != nil {
			logger.Error(err, "failed to build spoke client, skipping cluster", "cluster", clusterName)
			continue
		}

		vars, err := agenticrun.BuildVarMap(ctx, c, operatorNamespace, clusterName)
		if err != nil {
			logger.Error(err, "failed to build variable map, skipping cluster", "cluster", clusterName)
			continue
		}
		expanded := agenticrun.ExpandVariables(cfg, vars)

		labels := map[string]string{
			"app.kubernetes.io/managed-by":     "telco-anomaly-detection",
			"telco-anomaly.io/healthcheck-ref": thc.Name,
			"telco-anomaly.io/owner-namespace": thc.Namespace,
		}
		run, err := agenticrun.BuildObject(runName, labels, expanded)
		if err != nil {
			logger.Error(err, "failed to build AgenticRun object, skipping cluster", "cluster", clusterName)
			continue
		}
		if err := spokeClient.Create(ctx, run); err != nil {
			logger.Error(err, "failed to create AgenticRun", "cluster", clusterName, "name", runName)
			continue
		}

		logger.Info("created AgenticRun", "cluster", clusterName, "name", runName, "namespace", agenticrun.Namespace)
	}
	return nil
}

// buildSpokeClient creates a controller-runtime client from raw kubeconfig bytes.
// The client supports unstructured objects, which is sufficient for AgenticRun creation.
var buildSpokeClient = func(kubeconfigBytes []byte) (client.Client, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("building REST config: %w", err)
	}
	return newClientFromRESTConfig(cfg)
}

// newClientFromRESTConfig is a variable so tests can inject a fake client builder.
var newClientFromRESTConfig = func(cfg *rest.Config) (client.Client, error) {
	return client.New(cfg, client.Options{})
}
