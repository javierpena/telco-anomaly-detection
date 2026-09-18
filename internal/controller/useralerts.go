package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

const (
	userAlertManagedByLabel = "app.kubernetes.io/managed-by"
	userAlertManagedByValue = "telco-anomaly-detection"
	userAlertLabel          = "ran.openshift.io/user-managed-alert"
	userAlertLabelValue     = "true"
)

// UserAlertConfig holds the parsed contents of a user-defined alert ConfigMap.
type UserAlertConfig struct {
	AlertName     string
	AlertRule     string
	AlertMetrics  []string
	ConfigMapName string
}

// listUserAlertConfigs returns the parsed contents of all user-alert ConfigMaps in namespace.
// ConfigMaps must carry both userAlertManagedByLabel and userAlertLabel labels.
// Entries missing alertName or alertRule are logged and skipped.
func listUserAlertConfigs(ctx context.Context, c client.Client, namespace string) ([]UserAlertConfig, error) {
	logger := log.FromContext(ctx)

	var cmList corev1.ConfigMapList
	if err := c.List(ctx, &cmList,
		client.InNamespace(namespace),
		client.MatchingLabels{
			userAlertManagedByLabel: userAlertManagedByValue,
			userAlertLabel:          userAlertLabelValue,
		},
	); err != nil {
		return nil, err
	}

	var configs []UserAlertConfig
	for _, cm := range cmList.Items {
		name := cm.Data["alertName"]
		rule := cm.Data["alertRule"]
		if name == "" || rule == "" {
			logger.Info("user-alert ConfigMap missing alertName or alertRule, skipping",
				"configmap", cm.Name)
			continue
		}

		var metrics []string
		if raw := cm.Data["alertMetrics"]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &metrics); err != nil {
				logger.Info("user-alert ConfigMap has invalid alertMetrics JSON, skipping",
					"configmap", cm.Name, "error", fmt.Sprintf("%v", err))
				continue
			}
		}

		configs = append(configs, UserAlertConfig{
			AlertName:     name,
			AlertRule:     rule,
			AlertMetrics:  metrics,
			ConfigMapName: cm.Name,
		})
	}
	return configs, nil
}

// isUserAlertConfigMap returns true when obj carries both required user-alert labels.
func isUserAlertConfigMap(obj client.Object) bool {
	labels := obj.GetLabels()
	return labels[userAlertManagedByLabel] == userAlertManagedByValue &&
		labels[userAlertLabel] == userAlertLabelValue
}

// mapUserAlertCMToTHC maps a user-alert ConfigMap event to all TelcoHealthcheck CRs in
// the same namespace, so they are re-reconciled whenever a user-alert ConfigMap changes.
func (r *TelcoHealthcheckReconciler) mapUserAlertCMToTHC(
	ctx context.Context,
	obj client.Object,
) []ctrl.Request {
	logger := log.FromContext(ctx)

	list := &ranv1alpha1.TelcoHealthcheckList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		logger.Error(err, "failed to list TelcoHealthchecks on user-alert ConfigMap event")
		return nil
	}

	requests := make([]ctrl.Request, len(list.Items))
	for i, thc := range list.Items {
		requests[i] = ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&thc)}
	}
	logger.V(1).Info("mapped user-alert ConfigMap event to TelcoHealthcheck reconcile requests",
		"count", len(requests))
	return requests
}
