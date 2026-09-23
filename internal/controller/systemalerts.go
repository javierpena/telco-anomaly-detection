package controller

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

const (
	systemAlertLabel      = "ran.openshift.io/system-managed-alert"
	systemAlertLabelValue = "true"
)

//go:embed assets/alert-host-network.yaml
var alertHostNetworkYAML []byte

//go:embed assets/alert-pod-network.yaml
var alertPodNetworkYAML []byte

//go:embed assets/alert-host-reserved-cpu.yaml
var alertHostReservedCPUYAML []byte

//go:embed assets/alert-ovs-process-cpu.yaml
var alertOVSProcessCPUYAML []byte

type systemAlertAsset struct {
	yaml    []byte
	enabled func(ranv1alpha1.AlertsSpec) bool
}

var systemAlertAssets = []systemAlertAsset{
	{yaml: alertHostNetworkYAML, enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.HostNetwork }},
	{yaml: alertPodNetworkYAML, enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.PodNetwork }},
	{yaml: alertHostReservedCPUYAML, enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.HostReservedCPU }},
	{yaml: alertOVSProcessCPUYAML, enabled: func(a ranv1alpha1.AlertsSpec) bool { return a.OVSProcessCPU }},
}

// decodeSystemAlertCM decodes an embedded ConfigMap YAML asset into a corev1.ConfigMap.
func decodeSystemAlertCM(data []byte) (corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	if err := decoder.Decode(&cm); err != nil {
		return corev1.ConfigMap{}, fmt.Errorf("decoding system alert asset: %w", err)
	}
	return cm, nil
}

// reconcileSystemAlertConfigMaps creates or updates each system-alert ConfigMap in namespace
// when the corresponding spec boolean is true, and deletes it when false.
func reconcileSystemAlertConfigMaps(ctx context.Context, c client.Client, namespace string, alerts ranv1alpha1.AlertsSpec) error {
	logger := log.FromContext(ctx)

	for _, asset := range systemAlertAssets {
		cm, err := decodeSystemAlertCM(asset.yaml)
		if err != nil {
			return err
		}
		cm.Namespace = namespace

		if asset.enabled(alerts) {
			existing := &corev1.ConfigMap{}
			err := c.Get(ctx, client.ObjectKeyFromObject(&cm), existing)
			if errors.IsNotFound(err) {
				logger.Info("creating system alert ConfigMap", "name", cm.Name)
				if err := c.Create(ctx, &cm); err != nil {
					return fmt.Errorf("creating system alert ConfigMap %s: %w", cm.Name, err)
				}
				continue
			}
			if err != nil {
				return fmt.Errorf("getting system alert ConfigMap %s: %w", cm.Name, err)
			}
			existing.Data = cm.Data
			existing.Labels = cm.Labels
			logger.Info("updating system alert ConfigMap", "name", cm.Name)
			if err := c.Update(ctx, existing); err != nil {
				return fmt.Errorf("updating system alert ConfigMap %s: %w", cm.Name, err)
			}
		} else {
			existing := &corev1.ConfigMap{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(&cm), existing); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("getting system alert ConfigMap %s: %w", cm.Name, err)
			}
			logger.Info("deleting system alert ConfigMap", "name", cm.Name)
			if err := c.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("deleting system alert ConfigMap %s: %w", cm.Name, err)
			}
		}
	}
	return nil
}

// cleanupSystemAlertConfigMaps deletes all four system-alert ConfigMaps unconditionally.
// Called during TelcoHealthcheck CR deletion.
func cleanupSystemAlertConfigMaps(ctx context.Context, c client.Client, namespace string) error {
	logger := log.FromContext(ctx)

	for _, asset := range systemAlertAssets {
		cm, err := decodeSystemAlertCM(asset.yaml)
		if err != nil {
			return err
		}
		cm.Namespace = namespace

		existing := &corev1.ConfigMap{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(&cm), existing); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("getting system alert ConfigMap %s for cleanup: %w", cm.Name, err)
		}
		logger.Info("cleaning up system alert ConfigMap", "name", cm.Name)
		if err := c.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting system alert ConfigMap %s: %w", cm.Name, err)
		}
	}
	return nil
}
