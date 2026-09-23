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
	systemPeriodicLabel      = "ran.openshift.io/system-managed-periodic"
	systemPeriodicLabelValue = "true"
)

//go:embed assets/periodic-rds-compliance.yaml
var periodicRDSComplianceYAML []byte

type systemPeriodicAsset struct {
	yaml    []byte
	enabled func(ranv1alpha1.PeriodicHealthChecksSpec) bool
}

var systemPeriodicAssets = []systemPeriodicAsset{
	{yaml: periodicRDSComplianceYAML, enabled: func(p ranv1alpha1.PeriodicHealthChecksSpec) bool { return p.RDSCompliance.Enabled }},
}

func decodeSystemPeriodicCM(data []byte) (corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	if err := decoder.Decode(&cm); err != nil {
		return corev1.ConfigMap{}, fmt.Errorf("decoding system periodic asset: %w", err)
	}
	return cm, nil
}

// reconcileSystemPeriodicConfigMaps creates or updates each system-periodic ConfigMap in namespace
// when the corresponding spec boolean is true, and deletes it when false.
func reconcileSystemPeriodicConfigMaps(ctx context.Context, c client.Client, namespace string, periodic ranv1alpha1.PeriodicHealthChecksSpec) error {
	logger := log.FromContext(ctx)

	for _, asset := range systemPeriodicAssets {
		cm, err := decodeSystemPeriodicCM(asset.yaml)
		if err != nil {
			return err
		}
		cm.Namespace = namespace

		if asset.enabled(periodic) {
			existing := &corev1.ConfigMap{}
			err := c.Get(ctx, client.ObjectKeyFromObject(&cm), existing)
			if errors.IsNotFound(err) {
				logger.Info("creating system periodic ConfigMap", "name", cm.Name)
				if err := c.Create(ctx, &cm); err != nil {
					return fmt.Errorf("creating system periodic ConfigMap %s: %w", cm.Name, err)
				}
				continue
			}
			if err != nil {
				return fmt.Errorf("getting system periodic ConfigMap %s: %w", cm.Name, err)
			}
			existing.Data = cm.Data
			existing.Labels = cm.Labels
			logger.Info("updating system periodic ConfigMap", "name", cm.Name)
			if err := c.Update(ctx, existing); err != nil {
				return fmt.Errorf("updating system periodic ConfigMap %s: %w", cm.Name, err)
			}
		} else {
			existing := &corev1.ConfigMap{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(&cm), existing); err != nil {
				if errors.IsNotFound(err) {
					continue
				}
				return fmt.Errorf("getting system periodic ConfigMap %s: %w", cm.Name, err)
			}
			logger.Info("deleting system periodic ConfigMap", "name", cm.Name)
			if err := c.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("deleting system periodic ConfigMap %s: %w", cm.Name, err)
			}
		}
	}
	return nil
}

// cleanupSystemPeriodicConfigMaps deletes all system-periodic ConfigMaps unconditionally.
// Called during TelcoHealthcheck CR deletion.
func cleanupSystemPeriodicConfigMaps(ctx context.Context, c client.Client, namespace string) error {
	logger := log.FromContext(ctx)

	for _, asset := range systemPeriodicAssets {
		cm, err := decodeSystemPeriodicCM(asset.yaml)
		if err != nil {
			return err
		}
		cm.Namespace = namespace

		existing := &corev1.ConfigMap{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(&cm), existing); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("getting system periodic ConfigMap %s for cleanup: %w", cm.Name, err)
		}
		logger.Info("cleaning up system periodic ConfigMap", "name", cm.Name)
		if err := c.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting system periodic ConfigMap %s: %w", cm.Name, err)
		}
	}
	return nil
}
