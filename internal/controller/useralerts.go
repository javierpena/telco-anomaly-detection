package controller

import (
	"context"
	"encoding/json"

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

// UserAlertConfig holds the parsed contents of an alert ConfigMap (system or user-defined).
type UserAlertConfig struct {
	AlertName     string
	GroupName     string // from alertGroupName data key; empty for user alerts (builder defaults to telco-user-<alertname>)
	AlertRule     string
	AlertMetrics  []string
	ConfigMapName string
}

// parseAlertConfigMap extracts an UserAlertConfig from a ConfigMap's data fields.
// Returns false when alertName or alertRule are absent, or alertMetrics JSON is malformed.
func parseAlertConfigMap(cm corev1.ConfigMap) (UserAlertConfig, bool) {
	name := cm.Data["alertName"]
	rule := cm.Data["alertRule"]
	if name == "" || rule == "" {
		return UserAlertConfig{}, false
	}

	var metrics []string
	if raw := cm.Data["alertMetrics"]; raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &metrics); err != nil {
			return UserAlertConfig{}, false
		}
	}

	return UserAlertConfig{
		AlertName:     name,
		GroupName:     cm.Data["alertGroupName"],
		AlertRule:     rule,
		AlertMetrics:  metrics,
		ConfigMapName: cm.Name,
	}, true
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
		cfg, ok := parseAlertConfigMap(cm)
		if !ok {
			logger.Info("user-alert ConfigMap missing alertName, alertRule, or has invalid alertMetrics, skipping",
				"configmap", cm.Name)
			continue
		}
		configs = append(configs, cfg)
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
