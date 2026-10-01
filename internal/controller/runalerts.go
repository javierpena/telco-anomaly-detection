package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
)

const (
	alertRefreshInterval = time.Minute
	alertLifetime        = 5 * time.Minute
	consoleClaim         = "consoleurl.cluster.open-cluster-management.io"
)

// runAlertReconciler delivers the durable transition queue and renews firing
// alerts. Its run watch also replays outstanding work after an operator restart.
type runAlertReconciler struct {
	client.Client
	reader    client.Reader
	namespace string
	http      *http.Client
}

func (r *runAlertReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ranv1alpha1.TelcoHealthCheckRun{}).
		Watches(&ranv1alpha1.TelcoHealthcheck{}, handler.EnqueueRequestsFromMapFunc(r.mapHealthcheckToRuns),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapAlertSecretToRuns),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetNamespace() == r.namespace
			}))).
		Complete(r)
}

func (r *runAlertReconciler) mapAlertSecretToRuns(ctx context.Context, obj client.Object) []ctrl.Request {
	owner := &ranv1alpha1.TelcoHealthcheck{}
	if err := r.reader.Get(ctx, client.ObjectKey{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}, owner); err != nil {
		if !apierrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "checking Alertmanager credentials after Secret change")
		}
		return nil
	}
	config := owner.Spec.AlertManager
	if obj.GetNamespace() != r.namespace || config == nil || config.CredentialsSecret == nil ||
		config.CredentialsSecret.Name != obj.GetName() {
		return nil
	}
	return r.mapHealthcheckToRuns(ctx, owner)
}

func (r *runAlertReconciler) mapHealthcheckToRuns(ctx context.Context, _ client.Object) []ctrl.Request {
	var records ranv1alpha1.TelcoHealthCheckRunList
	if err := r.reader.List(ctx, &records, client.InNamespace(r.namespace)); err != nil {
		log.FromContext(ctx).Error(err, "listing run alerts after configuration change")
		return nil
	}
	requests := make([]ctrl.Request, 0, len(records.Items))
	for i := range records.Items {
		requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&records.Items[i])})
	}
	return requests
}

func (r *runAlertReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Namespace != r.namespace {
		return ctrl.Result{}, nil
	}
	owner := &ranv1alpha1.TelcoHealthcheck{}
	if err := r.reader.Get(ctx, client.ObjectKey{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}, owner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	record := &ranv1alpha1.TelcoHealthCheckRun{}
	if err := r.reader.Get(ctx, req.NamespacedName, record); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if owner.Spec.AlertManager == nil {
		// The URL is intentionally not retained: removing it stops all delivery.
		if len(record.Status.AlertNotifications) == 0 && record.Status.AlertStartsAt == nil && record.Status.LastAlertSentTime == nil {
			return ctrl.Result{}, nil
		}
		before := record.DeepCopy()
		record.Status.AlertNotifications = nil
		record.Status.AlertStartsAt = nil
		record.Status.LastAlertSentTime = nil
		return ctrl.Result{}, r.Status().Patch(ctx, record, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	}

	// Records created before this feature (or while delivery was disabled) need
	// an initial firing notification when configuration is enabled.
	if record.Status.AgenticRunActionRequired == "True" && record.Status.AlertStartsAt == nil {
		before := record.DeepCopy()
		now := metav1.Now()
		record.Status.AlertStartsAt = &now
		summary := ""
		if record.Status.AgenticRunStatus != nil {
			summary = record.Status.AgenticRunStatus.Summary
		}
		record.Status.AlertNotifications = append(record.Status.AlertNotifications, ranv1alpha1.AlertNotification{
			Firing: true, StartsAt: now, Summary: summary,
		})
		if err := r.Status().Patch(ctx, record, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if len(record.Status.AlertNotifications) > 0 {
		event := record.Status.AlertNotifications[0]
		if err := r.send(ctx, owner.Spec.AlertManager, record, event); err != nil {
			return ctrl.Result{}, err
		}
		before := record.DeepCopy()
		record.Status.AlertNotifications = record.Status.AlertNotifications[1:]
		if event.Firing && record.Status.AgenticRunActionRequired == "True" &&
			record.Status.AlertStartsAt != nil && record.Status.AlertStartsAt.Equal(&event.StartsAt) {
			now := metav1.Now()
			record.Status.LastAlertSentTime = &now
		}
		if err := r.Status().Patch(ctx, record, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			// The POST may have succeeded before a conflict or crash. Retrying
			// the same fingerprint and startsAt is safe.
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if record.Status.AgenticRunActionRequired != "True" || record.Status.AlertStartsAt == nil {
		return ctrl.Result{}, nil
	}
	if record.Status.LastAlertSentTime != nil {
		until := alertRefreshInterval - time.Since(record.Status.LastAlertSentTime.Time)
		if until > 0 {
			return ctrl.Result{RequeueAfter: until}, nil
		}
	}
	summary := ""
	if record.Status.AgenticRunStatus != nil {
		summary = record.Status.AgenticRunStatus.Summary
	}
	if err := r.send(ctx, owner.Spec.AlertManager, record, ranv1alpha1.AlertNotification{
		Firing: true, StartsAt: *record.Status.AlertStartsAt, Summary: summary,
	}); err != nil {
		return ctrl.Result{}, err
	}
	before := record.DeepCopy()
	now := metav1.Now()
	record.Status.LastAlertSentTime = &now
	if err := r.Status().Patch(ctx, record, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: alertRefreshInterval}, nil
}

type outgoingAlert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

func (r *runAlertReconciler) send(ctx context.Context, config *ranv1alpha1.AlertManagerSpec, record *ranv1alpha1.TelcoHealthCheckRun, event ranv1alpha1.AlertNotification) error {
	parsed, err := url.Parse(config.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return fmt.Errorf("invalid Alertmanager base URL")
	}
	endpoint := strings.TrimRight(config.URL, "/") + "/api/v2/alerts"
	annotations := map[string]string{
		"cluster": record.Status.ClusterName, "summary": event.Summary,
		"agentic_run": record.Status.AgenticRunName,
	}
	if link := r.agenticRunURL(ctx, record.Status.ClusterName, record.Status.AgenticRunName); link != "" {
		annotations["url"] = link
	}
	endsAt := time.Now().Add(alertLifetime).UTC()
	if !event.Firing {
		if event.EndsAt == nil {
			return fmt.Errorf("resolution for %s/%s is missing endsAt", record.Namespace, record.Name)
		}
		endsAt = event.EndsAt.Time
	}
	payload, err := json.Marshal([]outgoingAlert{{
		Labels: map[string]string{
			"alertname": "TelcoActionRequired", "severity": "warning",
			"cluster": record.Status.ClusterName, "agentic_run": record.Status.AgenticRunName,
		},
		Annotations: annotations, StartsAt: event.StartsAt.Time, EndsAt: endsAt,
	}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := r.authorize(ctx, req, config); err != nil {
		return err
	}
	// Never forward credentials to an endpoint supplied in a redirect.
	httpClient := *r.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting run alert: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Some gateways echo credentials in error bodies. Report only the
		// status so retries do not leak secrets into controller logs.
		return fmt.Errorf("posting run alert: Alertmanager returned %s", response.Status)
	}
	return nil
}

func (r *runAlertReconciler) authorize(ctx context.Context, req *http.Request, config *ranv1alpha1.AlertManagerSpec) error {
	authType := config.AuthType
	if authType == "" {
		authType = ranv1alpha1.AlertManagerAuthNone
	}
	if authType == ranv1alpha1.AlertManagerAuthNone {
		if config.CredentialsSecret != nil {
			return fmt.Errorf("Alertmanager credentialsSecret requires bearer or basic auth")
		}
		return nil
	}
	if authType != ranv1alpha1.AlertManagerAuthBearer && authType != ranv1alpha1.AlertManagerAuthBasic {
		return fmt.Errorf("unsupported Alertmanager authType %q", authType)
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("Alertmanager authentication requires HTTPS")
	}
	if config.CredentialsSecret == nil || config.CredentialsSecret.Name == "" {
		return fmt.Errorf("Alertmanager %s auth requires credentialsSecret.name", authType)
	}
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: r.namespace, Name: config.CredentialsSecret.Name}
	if err := r.reader.Get(ctx, key, secret); err != nil {
		return fmt.Errorf("reading Alertmanager credentials Secret %s/%s: %w", key.Namespace, key.Name, err)
	}
	if authType == ranv1alpha1.AlertManagerAuthBearer {
		token := secret.Data["token"]
		if len(token) == 0 {
			return fmt.Errorf("Alertmanager credentials Secret %s/%s has no token", key.Namespace, key.Name)
		}
		req.Header.Set("Authorization", "Bearer "+string(token))
		return nil
	}
	username, password := secret.Data["username"], secret.Data["password"]
	if len(username) == 0 || len(password) == 0 || bytes.ContainsRune(username, ':') {
		return fmt.Errorf("Alertmanager credentials Secret %s/%s requires non-empty username without ':' and non-empty password", key.Namespace, key.Name)
	}
	req.SetBasicAuth(string(username), string(password))
	return nil
}

func (r *runAlertReconciler) agenticRunURL(ctx context.Context, clusterName, runName string) string {
	cluster := &clusterv1.ManagedCluster{}
	if err := r.reader.Get(ctx, client.ObjectKey{Name: clusterName}, cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "reading spoke console claim", "cluster", clusterName)
		}
		return ""
	}
	for _, claim := range cluster.Status.ClusterClaims {
		if claim.Name == consoleClaim {
			base, err := url.Parse(claim.Value)
			if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
				return ""
			}
			base.Path = strings.TrimRight(base.Path, "/") + "/lightspeed/runs/" + agenticrun.Namespace + "/" + runName
			base.RawQuery = ""
			base.Fragment = ""
			return base.String()
		}
	}
	return ""
}
