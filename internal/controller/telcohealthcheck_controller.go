package controller

import (
	"context"
	"fmt"
	"time"

	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

const telcoHealthcheckFinalizer = "ran.openshift.io/telcohealthcheck-finalizer"

// +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthchecks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthchecks/finalizers,verbs=update
// +kubebuilder:rbac:groups=cluster.open-cluster-management.io,resources=managedclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// TelcoHealthcheckReconciler reconciles TelcoHealthcheck objects.
// It drives alert rule management, AlertManager webhook configuration, and periodic
// agentic health checks across ACM managed clusters.
type TelcoHealthcheckReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	OperatorNamespace   string
	AlertReceiverSvcURL string
	// LogLevel, when non-nil, is updated on each reconcile to reflect the most
	// verbose logLevel across all TelcoHealthcheck CRs.
	LogLevel *uberzap.AtomicLevel
}

// Reconcile is the main reconciliation loop. It is called when a TelcoHealthcheck object
// or a ManagedCluster object changes. It:
//  1. Resolves the set of monitored clusters.
//  2. Reconciles the Thanos custom alert rules ConfigMap.
//  3. Configures the AlertManager webhook receiver.
//  4. Triggers periodic agentic health checks when the configured period elapses.
//  5. Requeues the object so it wakes up when the next check is due.
func (r *TelcoHealthcheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling TelcoHealthcheck", "name", req.NamespacedName)

	thc := &ranv1alpha1.TelcoHealthcheck{}
	if err := r.Get(ctx, req.NamespacedName, thc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path: DeletionTimestamp is set, run cleanup then remove finalizer.
	if !thc.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, thc)
	}

	// Ensure finalizer is registered on every live CR.
	if !controllerutil.ContainsFinalizer(thc, telcoHealthcheckFinalizer) {
		controllerutil.AddFinalizer(thc, telcoHealthcheckFinalizer)
		if err := r.Update(ctx, thc); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		// Update triggers a new reconcile; return here to avoid a
		// resourceVersion conflict with the status update at the end.
		return ctrl.Result{}, nil
	}

	// Sync log level from all TelcoHealthcheck CRs — most verbose wins.
	// The informer cache makes r.List a local map lookup.
	if r.LogLevel != nil {
		allThc := &ranv1alpha1.TelcoHealthcheckList{}
		if listErr := r.List(ctx, allThc); listErr == nil {
			level := zapcore.InfoLevel
			if ranv1alpha1.IsDebugLevel(allThc.Items) {
				level = zapcore.DebugLevel
			}
			r.LogLevel.SetLevel(level)
		}
	}

	// Ensure AgenticRun configuration ConfigMaps exist (create-if-absent; never overwrite).
	if err := ensureAgenticRunConfigs(ctx, r.Client, r.OperatorNamespace); err != nil {
		logger.Error(err, "failed to ensure AgenticRun config ConfigMaps")
		return ctrl.Result{}, err
	}

	// Reconcile kube-compare-mcp infrastructure based on RDS compliance setting.
	if err := reconcileKubeCompareMCP(ctx, r.Client, r.OperatorNamespace,
		thc.Spec.PeriodicHealthChecks.RDSCompliance.Enabled); err != nil {
		logger.Error(err, "failed to reconcile kube-compare-mcp")
		return ctrl.Result{}, err
	}

	// Resolve monitored clusters.
	monitoredClusters, err := getMonitoredClusters(ctx, r.Client, thc.Spec.ManagedClusters)
	if err != nil {
		logger.Error(err, "failed to resolve monitored clusters")
		return ctrl.Result{}, fmt.Errorf("resolving monitored clusters: %w", err)
	}
	thc.Status.MonitoredClusters = monitoredClusters

	// Reconcile Thanos alert rules.
	if err := reconcileAlertRules(ctx, r.Client, thc.Spec); err != nil {
		logger.Error(err, "failed to reconcile alert rules")
		// Non-fatal: log and continue so the rest of the reconcile proceeds.
	}

	// Reconcile MCO custom metrics allowlist.
	if err := reconcileObservabilityMetrics(ctx, r.Client, thc.Spec.Alerts); err != nil {
		logger.Error(err, "failed to reconcile observability metrics allowlist")
		// Non-fatal: log and continue.
	}

	// Reconcile AlertManager webhook receiver.
	alertReceiverURL := r.alertReceiverURL(thc.Namespace)
	if err := reconcileAlertManagerReceiver(ctx, r.Client, alertReceiverURL); err != nil {
		logger.Error(err, "failed to reconcile AlertManager receiver")
		// Non-fatal: log and continue.
	}

	// Determine next requeue time based on periodic check schedules.
	requeueAfter, err := r.runPeriodicChecks(ctx, thc, monitoredClusters)
	if err != nil {
		logger.Error(err, "error during periodic check scheduling")
	}

	// Persist status changes.
	if err := r.Status().Update(ctx, thc); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	logger.Info("reconciliation complete", "requeueAfter", requeueAfter)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// handleDeletion runs cleanup and removes the finalizer so the API server can delete the object.
// Cleanup errors are logged but do not block finalizer removal — a stuck finalizer would prevent
// the object from ever being deleted if an external resource is already gone.
func (r *TelcoHealthcheckReconciler) handleDeletion(
	ctx context.Context,
	thc *ranv1alpha1.TelcoHealthcheck,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("handling deletion of TelcoHealthcheck", "name", thc.Name)

	if err := r.cleanupResources(ctx, thc); err != nil {
		logger.Error(err, "one or more cleanup steps failed; proceeding with finalizer removal")
	}

	controllerutil.RemoveFinalizer(thc, telcoHealthcheckFinalizer)
	if err := r.Update(ctx, thc); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// cleanupResources removes the AlertManager webhook receiver and deletes the Thanos alert rules
// ConfigMap. Both steps are attempted; the first error encountered is returned.
func (r *TelcoHealthcheckReconciler) cleanupResources(
	ctx context.Context,
	_ *ranv1alpha1.TelcoHealthcheck,
) error {
	logger := log.FromContext(ctx)
	var firstErr error

	if err := removeAlertManagerReceiver(ctx, r.Client); err != nil {
		logger.Error(err, "failed to remove AlertManager receiver")
		firstErr = err
	}
	if err := cleanupAlertRules(ctx, r.Client); err != nil {
		logger.Error(err, "failed to cleanup Thanos rules ConfigMap")
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := cleanupKubeCompareMCP(ctx, r.Client, r.OperatorNamespace); err != nil {
		logger.Error(err, "failed to cleanup kube-compare-mcp resources")
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := cleanupObservabilityMetrics(ctx, r.Client); err != nil {
		logger.Error(err, "failed to cleanup observability metrics allowlist")
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// runPeriodicChecks runs each enabled periodic sub-check whose period has elapsed
// and returns the duration until the next required reconcile.
// AgenticRuns are only created for sub-checks that are explicitly enabled; if all
// sub-checks are disabled no resources are created regardless of the period.
// A zero period means the entire periodic schedule is disabled.
func (r *TelcoHealthcheckReconciler) runPeriodicChecks(
	ctx context.Context,
	thc *ranv1alpha1.TelcoHealthcheck,
	monitoredClusters []string,
) (time.Duration, error) {
	logger := log.FromContext(ctx)
	now := metav1.Now()
	period := thc.Spec.PeriodicHealthChecks.Period.Duration

	// Zero period means disabled — skip all sub-checks and don't requeue.
	if period == 0 {
		logger.V(1).Info("periodic health checks disabled (period is zero)")
		return 0, nil
	}

	requeueAfter := period

	// RDS compliance check.
	rds := thc.Spec.PeriodicHealthChecks.RDSCompliance
	if rds.Enabled {
		rdsPeriod := period
		if rds.Period != nil {
			rdsPeriod = rds.Period.Duration
		}

		if shouldRunCheck(thc.Status.LastRDSComplianceRunTime, rdsPeriod) {
			logger.Info("running RDS compliance health checks")
			if err := createAgenticRunsForClusters(ctx, r.Client, thc, monitoredClusters, "rds-compliance"); err != nil {
				logger.Error(err, "error during RDS compliance checks")
			} else {
				thc.Status.LastRDSComplianceRunTime = &now
			}
		} else if thc.Status.LastRDSComplianceRunTime != nil {
			untilNext := rdsPeriod - time.Since(thc.Status.LastRDSComplianceRunTime.Time)
			if untilNext > 0 && untilNext < requeueAfter {
				requeueAfter = untilNext
			}
		}
	}

	return requeueAfter, nil
}

// shouldRunCheck returns true if lastRun is nil (never run) or the period has elapsed.
func shouldRunCheck(lastRun *metav1.Time, period time.Duration) bool {
	if lastRun == nil {
		return true
	}
	return time.Since(lastRun.Time) >= period
}

// alertReceiverURL returns the in-cluster service URL for the alert receiver.
func (r *TelcoHealthcheckReconciler) alertReceiverURL(namespace string) string {
	if r.AlertReceiverSvcURL != "" {
		return r.AlertReceiverSvcURL
	}
	ns := r.OperatorNamespace
	if ns == "" {
		ns = namespace
	}
	return fmt.Sprintf(
		"http://telco-anomaly-alert-receiver.%s.svc.cluster.local:8080/webhook", ns)
}

// SetupWithManager registers the controller with the manager and sets up watches on
// TelcoHealthcheck resources and ManagedCluster resources.
func (r *TelcoHealthcheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ranv1alpha1.TelcoHealthcheck{}).
		Watches(
			&clusterv1.ManagedCluster{},
			handler.EnqueueRequestsFromMapFunc(r.mapManagedClusterToTelcoHealthchecks),
		).
		Complete(r)
}

// mapManagedClusterToTelcoHealthchecks maps a ManagedCluster event to all TelcoHealthcheck
// resources so they are re-reconciled when cluster membership changes.
func (r *TelcoHealthcheckReconciler) mapManagedClusterToTelcoHealthchecks(
	ctx context.Context,
	_ client.Object,
) []ctrl.Request {
	logger := log.FromContext(ctx)

	list := &ranv1alpha1.TelcoHealthcheckList{}
	if err := r.List(ctx, list); err != nil {
		logger.Error(err, "failed to list TelcoHealthchecks on ManagedCluster event")
		return nil
	}

	requests := make([]ctrl.Request, len(list.Items))
	for i, thc := range list.Items {
		requests[i] = ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(&thc),
		}
	}
	logger.V(1).Info("mapped ManagedCluster event to TelcoHealthcheck reconcile requests", "count", len(requests))
	return requests
}
