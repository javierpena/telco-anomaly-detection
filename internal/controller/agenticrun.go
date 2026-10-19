package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
)

const (
	operatorNamespace = "telco-healthcheck-system"
)

// checkTypeConfigMaps maps a periodic check type to the ConfigMap that holds its AgenticRun config.
var checkTypeConfigMaps = map[string]string{
	"rds-compliance":    "telco-anomaly-rds-compliance-config",
	"low-latency-check": "telco-anomaly-low-latency-check-config",
}

// createAgenticRunsForClusters creates a hub AgenticRun targeting each listed cluster.
// Clusters that fail creation are logged and skipped;
// errors are non-fatal to allow the others to proceed.
func createAgenticRunsForClusters(
	ctx context.Context,
	c client.Client,
	thc *ranv1alpha1.TelcoHealthcheck,
	monitoredClusters []string,
	checkType string,
) error {
	cfg, err := loadRunConfigForCheck(ctx, c, checkType)
	if err != nil {
		return err
	}
	return createAgenticRunsForClustersWithConfig(ctx, c, thc, monitoredClusters, checkType, cfg, 0, 0, ctx)
}

func loadRunConfigForCheck(ctx context.Context, c client.Client, checkType string) (*agenticrun.RunConfig, error) {
	configMapName, ok := checkTypeConfigMaps[checkType]
	if !ok {
		return nil, fmt.Errorf("no AgenticRun ConfigMap configured for check type %q", checkType)
	}

	cfg, err := agenticrun.LoadRunConfig(ctx, c, configMapName, operatorNamespace)
	if err != nil {
		return nil, fmt.Errorf("loading AgenticRun config for check type %q: %w", checkType, err)
	}
	if cfg.Request == "" {
		return nil, fmt.Errorf("AgenticRun config for check type %q has empty request field", checkType)
	}
	return cfg, nil
}

func createAgenticRunsForClustersWithConfig(
	ctx context.Context,
	c client.Client,
	thc *ranv1alpha1.TelcoHealthcheck,
	monitoredClusters []string,
	checkType string,
	cfg *agenticrun.RunConfig,
	minJitter, maxJitter time.Duration,
	delayCtx context.Context,
) error {
	logger := log.FromContext(ctx)
	if minJitter < 0 || maxJitter < minJitter {
		return fmt.Errorf("invalid jitter window [%s, %s]", minJitter, maxJitter)
	}

	logger.Info("scheduling AgenticRun resources", "checkType", checkType, "clusterCount", len(monitoredClusters),
		"minJitter", minJitter, "maxJitter", maxJitter)

	runPrefix := fmt.Sprintf("telco-health-%s-%d", checkType, time.Now().UnixNano())

	for _, clusterName := range monitoredClusters {
		runName := periodicRunName(runPrefix, clusterName)
		if maxJitter == 0 {
			createAgenticRunForCluster(ctx, c, thc, clusterName, checkType, cfg, runName)
			continue
		}
		delay := randomJitter(minJitter, maxJitter)
		logger.Info("scheduled periodic AgenticRun", "cluster", clusterName, "checkType", checkType, "jitter", delay)
		go func(clusterName string) {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-delayCtx.Done():
				return
			case <-timer.C:
			}
			if delayCtx.Err() != nil {
				return
			}
			createAgenticRunForCluster(delayCtx, c, thc, clusterName, checkType, cfg, runName)
		}(clusterName)
	}
	return nil
}

// periodicRunName fits Kubernetes' DNS subdomain limit while retaining a
// readable cluster prefix and a stable digest of the entire cluster identity.
func periodicRunName(prefix, cluster string) string {
	sum := sha256.Sum256([]byte(cluster))
	clusterPart := strings.TrimRight(cluster, "-.")
	if len(clusterPart) > 40 {
		clusterPart = strings.TrimRight(clusterPart[:40], "-.")
	}
	suffix := fmt.Sprintf("-%s-%x", clusterPart, sum[:8])
	if len(prefix)+len(suffix) > 253 {
		prefix = strings.TrimRight(prefix[:253-len(suffix)], "-.")
	}
	return prefix + suffix
}

func randomJitter(minJitter, maxJitter time.Duration) time.Duration {
	// Int63 covers the only case where window+1 would overflow.
	window := maxJitter - minJitter
	if window == time.Duration(1<<63-1) {
		return minJitter + time.Duration(rand.Int63())
	}
	return minJitter + time.Duration(rand.Int63n(int64(window)+1))
}

// createAgenticRunForCluster persists the run identity before creating the hub
// object, preserving recovery behavior for ambiguous create errors.
func createAgenticRunForCluster(
	ctx context.Context, c client.Client, thc *ranv1alpha1.TelcoHealthcheck,
	clusterName, checkType string, cfg *agenticrun.RunConfig, runName string,
) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("processing cluster", "cluster", clusterName, "checkType", checkType)

	vars, err := agenticrun.BuildVarMap(ctx, c, operatorNamespace, clusterName)
	if err != nil {
		logger.Error(err, "failed to build variable map, skipping cluster", "cluster", clusterName)
		return
	}
	expanded := agenticrun.ExpandVariables(cfg, vars)

	labels := map[string]string{
		"app.kubernetes.io/managed-by":     "telco-anomaly-detection",
		"telco-anomaly.io/healthcheck-ref": thc.Name,
	}
	run, err := agenticrun.BuildObject(runName, clusterName, labels, expanded)
	if err != nil {
		logger.Error(err, "failed to build AgenticRun object, skipping cluster", "cluster", clusterName)
		return
	}
	recordName := runName
	if _, err := healthcheckrun.Begin(ctx, c, operatorNamespace, recordName, runName,
		clusterName, ranv1alpha1.TriggerTypePeriodicHealthCheck, checkType, thc); err != nil {
		logger.Error(err, "failed to persist run identity, skipping hub creation", "cluster", clusterName)
		return
	}
	if err := c.Create(ctx, run); err != nil {
		if healthcheckrun.DefinitiveCreateError(err) {
			if statusErr := healthcheckrun.Fail(ctx, c, operatorNamespace, recordName); statusErr != nil {
				logger.Error(statusErr, "failed to record hub creation failure; pending record will be checked", "cluster", clusterName, "name", runName)
			}
		}
		logger.Error(err, "failed to create AgenticRun", "cluster", clusterName, "name", runName)
		return
	}
	if err := healthcheckrun.Confirm(ctx, c, operatorNamespace, recordName); err != nil {
		logger.Error(err, "failed to confirm hub record; controller will retry", "cluster", clusterName, "name", runName)
	}

	logger.Info("created AgenticRun", "cluster", clusterName, "name", runName, "namespace", agenticrun.Namespace)
}
