package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/healthcheckrun"
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
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get
// +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthcheckruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ran.openshift.io,resources=telcohealthcheckruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete

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
	LogLevel     *uberzap.AtomicLevel
	SpokeWatches *SpokeWatchManager
	// ShutdownContext cancels delayed periodic runs when the manager stops.
	ShutdownContext context.Context
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
		if r.SpokeWatches != nil {
			r.SpokeWatches.Stop()
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion path: DeletionTimestamp is set, run cleanup then remove finalizer.
	if !thc.DeletionTimestamp.IsZero() {
		if r.SpokeWatches != nil {
			r.SpokeWatches.Stop()
		}
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

	// Sync log level from the singleton CR.
	if r.LogLevel != nil {
		level := zapcore.InfoLevel
		if thc.Spec.LogLevel == ranv1alpha1.LogLevelDebug {
			level = zapcore.DebugLevel
		}
		r.LogLevel.SetLevel(level)
	}
	// Status tracking and recovery must continue even if reconciliation of an
	// unrelated managed resource fails later in this pass.
	monitoredClusters, err := getMonitoredClusters(ctx, r.Client, thc.Spec.ManagedClusters)
	if err != nil {
		logger.Error(err, "failed to resolve monitored clusters")
		return ctrl.Result{}, fmt.Errorf("resolving monitored clusters: %w", err)
	}
	thc.Status.MonitoredClusters = monitoredClusters
	if r.SpokeWatches != nil {
		r.SpokeWatches.Sync(ctx, monitoredClusters)
	}
	pendingRuns, err := recoverPendingRuns(ctx, r.Client, r.OperatorNamespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile system-alert ConfigMaps from embedded assets (create/update when enabled, delete when disabled).
	// Must run before reconcileAlertRules so the rule listing sees up-to-date system ConfigMaps.
	if err := reconcileSystemAlertConfigMaps(ctx, r.Client, r.OperatorNamespace, thc.Spec.Alerts); err != nil {
		logger.Error(err, "failed to reconcile system alert ConfigMaps")
		return ctrl.Result{}, err
	}

	// Reconcile system-periodic ConfigMaps from embedded assets (create/update when enabled, delete when disabled).
	if err := reconcileSystemPeriodicConfigMaps(ctx, r.Client, r.OperatorNamespace, thc.Spec.PeriodicHealthChecks); err != nil {
		logger.Error(err, "failed to reconcile system periodic ConfigMaps")
		return ctrl.Result{}, err
	}

	// Reconcile kube-compare-mcp infrastructure based on RDS compliance setting.
	if err := reconcileKubeCompareMCP(ctx, r.Client, r.OperatorNamespace,
		thc.Spec.PeriodicHealthChecks.RDSCompliance.Enabled); err != nil {
		logger.Error(err, "failed to reconcile kube-compare-mcp")
		return ctrl.Result{}, err
	}
	if err := reconcilePurgeCronJob(ctx, r.Client, r.OperatorNamespace, thc); err != nil {
		logger.Error(err, "failed to reconcile purge CronJob")
		return ctrl.Result{}, err
	}

	// Reconcile Thanos alert rules (lists system and user ConfigMaps internally).
	if err := reconcileAlertRules(ctx, r.Client, r.OperatorNamespace, thc.Spec.Alerts.UserAlerts); err != nil {
		logger.Error(err, "failed to reconcile alert rules")
		// Non-fatal: log and continue so the rest of the reconcile proceeds.
	}

	// Reconcile MCO custom metrics allowlist (lists system and user ConfigMaps internally).
	if err := reconcileObservabilityMetrics(ctx, r.Client, r.OperatorNamespace, thc.Spec.Alerts.UserAlerts); err != nil {
		logger.Error(err, "failed to reconcile observability metrics allowlist")
		// Non-fatal: log and continue.
	}

	// Reconcile AlertManager webhook receiver.
	alertReceiverURL := r.alertReceiverURL()
	if err := reconcileAlertManagerReceiver(ctx, r.Client, alertReceiverURL); err != nil {
		logger.Error(err, "failed to reconcile AlertManager receiver")
		// Non-fatal: log and continue.
	}

	// Determine next requeue time based on periodic check schedules.
	requeueAfter, periodicStatusPersisted, err := r.runPeriodicChecks(ctx, thc, monitoredClusters)
	if err != nil {
		logger.Error(err, "error during periodic check scheduling")
		return ctrl.Result{}, err
	}
	if pendingRuns && (requeueAfter == 0 || requeueAfter > time.Minute) {
		requeueAfter = time.Minute
	}
	// The manager runnable and controller may start concurrently. If the
	// runnable has not received its lifecycle context yet, retry the sync.
	if r.SpokeWatches != nil && !r.SpokeWatches.Ready() && (requeueAfter == 0 || requeueAfter > 5*time.Second) {
		requeueAfter = 5 * time.Second
	}

	// Persist status changes not already written as the periodic schedule claim.
	if !periodicStatusPersisted {
		if err := r.Status().Update(ctx, thc); err != nil {
			logger.Error(err, "failed to update status")
			return ctrl.Result{}, err
		}
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
	if err := cleanupSystemAlertConfigMaps(ctx, r.Client, r.OperatorNamespace); err != nil {
		logger.Error(err, "failed to cleanup system alert ConfigMaps")
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := cleanupSystemPeriodicConfigMaps(ctx, r.Client, r.OperatorNamespace); err != nil {
		logger.Error(err, "failed to cleanup system periodic ConfigMaps")
		if firstErr == nil {
			firstErr = err
		}
	}
	if err := cleanupPurgeCronJob(ctx, r.Client, r.OperatorNamespace); err != nil {
		logger.Error(err, "failed to cleanup purge CronJob")
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
) (time.Duration, bool, error) {
	logger := log.FromContext(ctx)
	now := metav1.Now()
	period := thc.Spec.PeriodicHealthChecks.Period.Duration

	// Zero period means disabled — skip all sub-checks and don't requeue.
	if period == 0 {
		logger.V(1).Info("periodic health checks disabled (period is zero)")
		return 0, false, nil
	}

	requeueAfter := period
	statusPersisted := false

	// RDS compliance check.
	rds := thc.Spec.PeriodicHealthChecks.RDSCompliance
	if rds.Enabled {
		minJitter, maxJitter, err := resolvePeriodicJitter(thc.Spec.PeriodicHealthChecks,
			"rds-compliance", rds.MinJitter, rds.MaxJitter)
		if err != nil {
			return requeueAfter, false, err
		}
		rdsPeriod := period
		if rds.Period != nil {
			rdsPeriod = rds.Period.Duration
		}

		if shouldRunCheck(thc.Status.LastRDSComplianceRunTime, rdsPeriod) {
			logger.Info("running RDS compliance health checks")
			cfg, err := loadRunConfigForCheck(ctx, r.Client, "rds-compliance")
			if err != nil {
				return requeueAfter, false, err
			}

			// Persist the schedule claim before making the non-transactional spoke
			// create. A queued reconcile can have a stale cache entry; the status
			// update's resourceVersion check makes it stop before creating a second
			// AgenticRun if another reconcile already claimed this interval.
			thc.Status.LastRDSComplianceRunTime = &now
			if err := r.Status().Update(ctx, thc); err != nil {
				return requeueAfter, false, fmt.Errorf("persisting RDS compliance schedule claim: %w", err)
			}
			statusPersisted = true

			delayCtx := r.ShutdownContext
			if delayCtx == nil {
				delayCtx = ctx
			}
			if err := createAgenticRunsForClustersWithConfig(ctx, r.Client, thc.DeepCopy(), monitoredClusters,
				"rds-compliance", cfg, minJitter, maxJitter, delayCtx); err != nil {
				return requeueAfter, statusPersisted, fmt.Errorf("creating RDS compliance AgenticRuns: %w", err)
			}
		} else if thc.Status.LastRDSComplianceRunTime != nil {
			untilNext := rdsPeriod - time.Since(thc.Status.LastRDSComplianceRunTime.Time)
			if untilNext > 0 && untilNext < requeueAfter {
				requeueAfter = untilNext
			}
		}
	}

	return requeueAfter, statusPersisted, nil
}

// resolvePeriodicJitter applies global defaults and independent per-check
// overrides, then validates both windows if admission validation was bypassed.
func resolvePeriodicJitter(p ranv1alpha1.PeriodicHealthChecksSpec, checkName string,
	checkMin, checkMax *metav1.Duration,
) (time.Duration, time.Duration, error) {
	globalMin, globalMax := 30*time.Second, 5*time.Minute
	if p.MinJitter != nil {
		globalMin = p.MinJitter.Duration
	}
	if p.MaxJitter != nil {
		globalMax = p.MaxJitter.Duration
	}
	if globalMin < 0 || globalMax < globalMin {
		return 0, 0, fmt.Errorf("invalid global jitter window [%s, %s]", globalMin, globalMax)
	}
	minJitter, maxJitter := globalMin, globalMax
	if checkMin != nil {
		minJitter = checkMin.Duration
	}
	if checkMax != nil {
		maxJitter = checkMax.Duration
	}
	if minJitter < 0 || maxJitter < minJitter {
		return 0, 0, fmt.Errorf("invalid %s jitter window [%s, %s]", checkName, minJitter, maxJitter)
	}
	return minJitter, maxJitter, nil
}

// shouldRunCheck returns true if lastRun is nil (never run) or the period has elapsed.
func shouldRunCheck(lastRun *metav1.Time, period time.Duration) bool {
	if lastRun == nil {
		return true
	}
	return time.Since(lastRun.Time) >= period
}

// alertReceiverURL returns the in-cluster service URL for the alert receiver.
func (r *TelcoHealthcheckReconciler) alertReceiverURL() string {
	if r.AlertReceiverSvcURL != "" {
		return r.AlertReceiverSvcURL
	}
	return fmt.Sprintf(
		"http://telco-anomaly-alert-receiver.%s.svc.cluster.local:8080/webhook",
		r.OperatorNamespace)
}

// SetupWithManager registers the controller with the manager and sets up watches on
// TelcoHealthcheck resources, ManagedCluster resources, and user-alert ConfigMaps.
func (r *TelcoHealthcheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.SpokeWatches = newSpokeWatchManager(mgr.GetClient(), mgr.GetAPIReader(), r.OperatorNamespace)
	if err := mgr.Add(r.SpokeWatches); err != nil {
		return err
	}
	if err := (&runAlertReconciler{
		Client: mgr.GetClient(), reader: mgr.GetAPIReader(), namespace: r.OperatorNamespace,
		http: &http.Client{Timeout: 10 * time.Second},
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&ranv1alpha1.TelcoHealthcheck{}).
		Watches(
			&clusterv1.ManagedCluster{},
			handler.EnqueueRequestsFromMapFunc(r.mapManagedClusterToTelcoHealthchecks),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.mapUserAlertCMToTHC),
			builder.WithPredicates(predicate.NewPredicateFuncs(isUserAlertConfigMap)),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.mapManagedClusterToTelcoHealthchecks),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return strings.TrimSuffix(obj.GetName(), "-admin-kubeconfig") == obj.GetNamespace() &&
					strings.HasSuffix(obj.GetName(), "-admin-kubeconfig")
			})),
		).
		Watches(
			&ranv1alpha1.TelcoHealthCheckRun{},
			handler.EnqueueRequestsFromMapFunc(r.mapManagedClusterToTelcoHealthchecks),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetAnnotations()[healthcheckrun.PendingAnnotation] == "true"
			})),
		).
		Complete(r)
}

// mapManagedClusterToTelcoHealthchecks maps a ManagedCluster event to the singleton
// TelcoHealthcheck CR so it is re-reconciled when cluster membership changes.
func (r *TelcoHealthcheckReconciler) mapManagedClusterToTelcoHealthchecks(
	_ context.Context,
	_ client.Object,
) []ctrl.Request {
	return []ctrl.Request{{
		NamespacedName: types.NamespacedName{Name: ranv1alpha1.TelcoHealthcheckCanonicalName},
	}}
}
