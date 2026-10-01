package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

func TestQueueActionAlertTransitions(t *testing.T) {
	status := ranv1alpha1.TelcoHealthCheckRunStatus{
		AgenticRunStatus: &ranv1alpha1.AgenticRunStatus{Summary: "check network"},
	}
	previous := *status.DeepCopy()
	status.AgenticRunActionRequired = "True"
	queueActionAlert(&status, previous)
	if len(status.AlertNotifications) != 1 || !status.AlertNotifications[0].Firing ||
		status.AlertNotifications[0].Summary != "check network" || status.AlertStartsAt == nil {
		t.Fatalf("missing firing transition: %+v", status)
	}
	previous = *status.DeepCopy()
	queueActionAlert(&status, previous)
	if len(status.AlertNotifications) != 1 {
		t.Fatal("replayed status queued a duplicate firing alert")
	}
	status.AgenticRunActionRequired = ""
	queueActionAlert(&status, previous)
	if len(status.AlertNotifications) != 2 || status.AlertNotifications[1].Firing ||
		status.AlertNotifications[1].EndsAt == nil || status.AlertStartsAt != nil ||
		!status.AlertNotifications[1].StartsAt.Equal(&status.AlertNotifications[0].StartsAt) {
		t.Fatalf("missing resolution on True to absent: %+v", status)
	}
	previous = *status.DeepCopy()
	status.AgenticRunActionRequired = "False"
	queueActionAlert(&status, previous)
	if len(status.AlertNotifications) != 2 {
		t.Fatal("absent to False queued another resolution")
	}
	previous = *status.DeepCopy()
	status.AgenticRunActionRequired = "True"
	queueActionAlert(&status, previous)
	previous = *status.DeepCopy()
	status.AgenticRunActionRequired = "False"
	queueActionAlert(&status, previous)
	if len(status.AlertNotifications) != 4 || status.AlertNotifications[3].Firing || status.AlertNotifications[3].EndsAt == nil {
		t.Fatalf("missing resolution on True to False: %+v", status.AlertNotifications)
	}
}

func TestRunAlertDeliveryRetryResolutionAndRenewal(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var received []outgoingAlert
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/v2/alerts" || req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		var alerts []outgoingAlert
		if err := json.NewDecoder(req.Body).Decode(&alerts); err != nil || len(alerts) != 1 {
			t.Errorf("invalid alert body: %v, count %d", err, len(alerts))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		received = append(received, alerts[0])
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}}
	owner.Spec.AlertManager = &ranv1alpha1.AlertManagerSpec{URL: server.URL} // omitted authType defaults to none
	cluster := makeManagedCluster("cluster-a")
	cluster.Status.ClusterClaims = []clusterv1.ManagedClusterClaim{{Name: consoleClaim, Value: "https://console.example.test/"}}
	start := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	record := &ranv1alpha1.TelcoHealthCheckRun{
		ObjectMeta: metav1.ObjectMeta{Name: "record", Namespace: operatorNamespace},
		Status: ranv1alpha1.TelcoHealthCheckRunStatus{
			ClusterName: "cluster-a", AgenticRunName: "run-a", AgenticRunActionRequired: "True",
			AgenticRunStatus:   &ranv1alpha1.AgenticRunStatus{Summary: "inspect pod"},
			AlertStartsAt:      &start,
			AlertNotifications: []ranv1alpha1.AlertNotification{{Firing: true, StartsAt: start, Summary: "inspect pod"}},
		},
	}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record, owner, cluster).
		WithObjects(owner, cluster, record).Build()
	makeReconciler := func() *runAlertReconciler {
		return &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace, http: server.Client()}
	}
	r := makeReconciler()
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(record)}
	if _, err := r.Reconcile(ctx, request); err == nil {
		t.Fatal("expected temporary Alertmanager failure")
	}
	assertQueued := func(want int) *ranv1alpha1.TelcoHealthCheckRun {
		t.Helper()
		current := &ranv1alpha1.TelcoHealthCheckRun{}
		if err := hub.Get(ctx, request.NamespacedName, current); err != nil {
			t.Fatal(err)
		}
		if len(current.Status.AlertNotifications) != want {
			t.Fatalf("queue length = %d, want %d", len(current.Status.AlertNotifications), want)
		}
		return current
	}
	assertQueued(1)
	mu.Lock()
	fail = false
	mu.Unlock()
	r = makeReconciler() // A new process can recover the stored queue.
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	current := assertQueued(0)
	if current.Status.LastAlertSentTime == nil {
		t.Fatal("firing delivery was not acknowledged")
	}
	mu.Lock()
	first := received[0]
	mu.Unlock()
	if first.Labels["alertname"] != "TelcoActionRequired" || first.Labels["severity"] != "warning" ||
		first.Labels["cluster"] != "cluster-a" || first.Labels["agentic_run"] != "run-a" ||
		first.Annotations["cluster"] != "cluster-a" || first.Annotations["summary"] != "inspect pod" ||
		first.Annotations["agentic_run"] != "run-a" ||
		first.Annotations["url"] != "https://console.example.test/lightspeed/runs/openshift-lightspeed/run-a" ||
		!first.StartsAt.Equal(start.Time) || time.Until(first.EndsAt) < 4*time.Minute {
		t.Fatalf("wrong firing payload: %+v", first)
	}
	if result, err := r.Reconcile(ctx, request); err != nil || result.RequeueAfter <= 0 {
		t.Fatalf("expected scheduled renewal, got %v, %v", result, err)
	}
	mu.Lock()
	if len(received) != 1 {
		t.Fatal("firing alert was resent before refresh")
	}
	mu.Unlock()
	old := current.DeepCopy()
	past := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	current.Status.LastAlertSentTime = &past
	if err := hub.Status().Patch(ctx, current, client.MergeFrom(old)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(received) != 2 || !received[1].StartsAt.Equal(first.StartsAt) {
		t.Fatalf("renewal changed alert identity or did not send: %+v", received)
	}
	mu.Unlock()
	current = assertQueued(0)
	old = current.DeepCopy()
	current.Status.AgenticRunActionRequired = "False"
	queueActionAlert(&current.Status, old.Status)
	if err := hub.Status().Patch(ctx, current, client.MergeFrom(old)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	assertQueued(0)
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 3 || received[2].EndsAt.After(time.Now().Add(time.Second)) || !received[2].EndsAt.After(first.StartsAt) || !received[2].StartsAt.Equal(first.StartsAt) {
		t.Fatalf("missing resolution for original firing alert: %+v", received)
	}
}

func TestRunAlertDisabledAndMissingConsoleClaim(t *testing.T) {
	ctx := context.Background()
	var received []outgoingAlert
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var alerts []outgoingAlert
		if err := json.NewDecoder(req.Body).Decode(&alerts); err != nil {
			t.Error(err)
		}
		received = append(received, alerts...)
	}))
	defer server.Close()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}}
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{Name: "record", Namespace: operatorNamespace},
		Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "cluster-a", AgenticRunName: "run-a", AgenticRunActionRequired: "True",
			AlertNotifications: []ranv1alpha1.AlertNotification{{Firing: true, StartsAt: metav1.Now()}}}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record, owner).
		WithObjects(owner, record).Build()
	r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace, http: server.Client()}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(record)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := hub.Get(ctx, request.NamespacedName, record); err != nil {
		t.Fatal(err)
	}
	if len(received) != 0 || len(record.Status.AlertNotifications) != 0 {
		t.Fatal("disabled destination sent an alert or kept stale transitions")
	}
	owner.Spec.AlertManager = &ranv1alpha1.AlertManagerSpec{URL: server.URL}
	if err := hub.Update(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 {
		t.Fatalf("re-enabling alerts did not notify current True state: %d", len(received))
	}
	if _, ok := received[0].Annotations["url"]; ok {
		t.Fatalf("missing console claim should omit URL: %+v", received[0].Annotations)
	}
}

func TestRunAlertAuthenticationAndRotation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		authType ranv1alpha1.AlertManagerAuthType
		data     map[string][]byte
		want     string
		rotated  map[string][]byte
		newWant  string
	}{
		{name: "default none"},
		{name: "bearer", authType: ranv1alpha1.AlertManagerAuthBearer,
			data: map[string][]byte{"token": []byte("old-token")}, want: "Bearer old-token",
			rotated: map[string][]byte{"token": []byte("new-token")}, newWant: "Bearer new-token"},
		{name: "basic", authType: ranv1alpha1.AlertManagerAuthBasic,
			data:    map[string][]byte{"username": []byte("operator"), "password": []byte("old-password")},
			want:    "Basic b3BlcmF0b3I6b2xkLXBhc3N3b3Jk",
			rotated: map[string][]byte{"username": []byte("operator"), "password": []byte("new-password")},
			newWant: "Basic b3BlcmF0b3I6bmV3LXBhc3N3b3Jk"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var headers []string
			var mu sync.Mutex
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				headers = append(headers, req.Header.Get("Authorization"))
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}}
			owner.Spec.AlertManager = &ranv1alpha1.AlertManagerSpec{URL: server.URL, AuthType: tt.authType}
			objects := []client.Object{owner}
			var secret *corev1.Secret
			if tt.data != nil {
				owner.Spec.AlertManager.CredentialsSecret = &ranv1alpha1.AlertManagerCredentialsSecret{Name: "outbound-alerts"}
				secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "outbound-alerts"}, Data: tt.data}
				objects = append(objects, secret)
			}
			start := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
			record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "run"},
				Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke", AgenticRunName: "run", AgenticRunActionRequired: "True",
					AlertStartsAt: &start, AlertNotifications: []ranv1alpha1.AlertNotification{{Firing: true, StartsAt: start}}}}
			objects = append(objects, record)
			hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).
				WithObjects(objects...).Build()
			r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace, http: server.Client()}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(record)}
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			firstHeaders := append([]string(nil), headers...)
			mu.Unlock()
			if len(firstHeaders) != 1 || firstHeaders[0] != tt.want {
				t.Fatalf("unexpected Authorization header: %q, want %q", firstHeaders, tt.want)
			}
			if secret == nil {
				return
			}
			secret.Data = tt.rotated
			if err := hub.Update(ctx, secret); err != nil {
				t.Fatal(err)
			}
			mapped := r.mapAlertSecretToRuns(ctx, secret)
			if len(mapped) != 1 || mapped[0] != request {
				t.Fatalf("Secret rotation did not enqueue the run: %+v", mapped)
			}
			if mapped := r.mapAlertSecretToRuns(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Namespace: operatorNamespace, Name: "unrelated",
			}}); len(mapped) != 0 {
				t.Fatalf("unrelated Secret enqueued runs: %+v", mapped)
			}
			current := &ranv1alpha1.TelcoHealthCheckRun{}
			if err := hub.Get(ctx, request.NamespacedName, current); err != nil {
				t.Fatal(err)
			}
			before := current.DeepCopy()
			past := metav1.NewTime(time.Now().Add(-2 * time.Minute))
			current.Status.LastAlertSentTime = &past
			if err := hub.Status().Patch(ctx, current, client.MergeFrom(before)); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			rotatedHeaders := append([]string(nil), headers...)
			mu.Unlock()
			if len(rotatedHeaders) != 2 || rotatedHeaders[1] != tt.newWant {
				t.Fatalf("renewal did not use rotated credentials: %q, want %q", rotatedHeaders, tt.newWant)
			}
		})
	}
}

func TestRunAlertAuthenticationErrorsKeepNotification(t *testing.T) {
	for _, tt := range []struct {
		name     string
		authType ranv1alpha1.AlertManagerAuthType
		data     map[string][]byte
		secret   bool
		useHTTP  bool
	}{
		{name: "bearer needs HTTPS", authType: ranv1alpha1.AlertManagerAuthBearer, secret: true,
			data: map[string][]byte{"token": []byte("private-token")}, useHTTP: true},
		{name: "bearer missing Secret", authType: ranv1alpha1.AlertManagerAuthBearer, secret: true},
		{name: "bearer missing token", authType: ranv1alpha1.AlertManagerAuthBearer, secret: true, data: map[string][]byte{"wrong": []byte("secret")}},
		{name: "basic missing password", authType: ranv1alpha1.AlertManagerAuthBasic, secret: true, data: map[string][]byte{"username": []byte("private-user")}},
		{name: "basic colon in username", authType: ranv1alpha1.AlertManagerAuthBasic, secret: true,
			data: map[string][]byte{"username": []byte("private:user"), "password": []byte("private-password")}},
		{name: "basic needs Secret reference", authType: ranv1alpha1.AlertManagerAuthBasic},
		{name: "none rejects Secret reference", secret: true},
		{name: "unknown authType", authType: "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var requests atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1) })
			server := httptest.NewTLSServer(handler)
			if tt.useHTTP {
				server.Close()
				server = httptest.NewServer(handler)
			}
			defer server.Close()
			owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName}}
			owner.Spec.AlertManager = &ranv1alpha1.AlertManagerSpec{URL: server.URL, AuthType: tt.authType}
			objects := []client.Object{owner}
			if tt.secret {
				owner.Spec.AlertManager.CredentialsSecret = &ranv1alpha1.AlertManagerCredentialsSecret{Name: "outbound-alerts"}
			}
			if tt.data != nil {
				objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
					Namespace: operatorNamespace, Name: "outbound-alerts",
				}, Data: tt.data})
			}
			record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "run"},
				Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke", AgenticRunName: "run",
					AlertNotifications: []ranv1alpha1.AlertNotification{{Firing: true, StartsAt: metav1.Now()}}}}
			objects = append(objects, record)
			hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).
				WithObjects(objects...).Build()
			r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace, http: server.Client()}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(record)}); err == nil {
				t.Fatal("invalid credentials or configuration was accepted")
			} else if strings.Contains(err.Error(), "private-") {
				t.Fatalf("credentials leaked into error: %v", err)
			}
			if requests.Load() != 0 {
				t.Fatal("request was sent without valid authentication")
			}
			current := &ranv1alpha1.TelcoHealthCheckRun{}
			if err := hub.Get(ctx, client.ObjectKeyFromObject(record), current); err != nil {
				t.Fatal(err)
			}
			if len(current.Status.AlertNotifications) != 1 {
				t.Fatal("failed authentication dropped queued alert")
			}
		})
	}
}

func TestRunAlertCredentialsNotForwardedOnRedirect(t *testing.T) {
	ctx := context.Background()
	var forwarded atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		forwarded.Store(req.Header.Get("Authorization") != "")
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer private-token" {
			t.Errorf("source did not receive bearer token: %q", got)
		}
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "outbound-alerts"},
		Data: map[string][]byte{"token": []byte("private-token")}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(secret).Build()
	r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace, http: source.Client()}
	config := &ranv1alpha1.AlertManagerSpec{URL: source.URL, AuthType: ranv1alpha1.AlertManagerAuthBearer,
		CredentialsSecret: &ranv1alpha1.AlertManagerCredentialsSecret{Name: secret.Name}}
	record := &ranv1alpha1.TelcoHealthCheckRun{Status: ranv1alpha1.TelcoHealthCheckRunStatus{
		ClusterName: "spoke", AgenticRunName: "run",
	}}
	if err := r.send(ctx, config, record, ranv1alpha1.AlertNotification{Firing: true, StartsAt: metav1.Now()}); err == nil {
		t.Fatal("redirect was accepted as successful delivery")
	}
	if forwarded.Load() {
		t.Fatal("credentials were forwarded to the redirect destination")
	}
}

func TestRunAlertUnauthorizedRetriedAfterSecretRotation(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer corrected-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("invalid token: private-token"))
		}
	}))
	defer server.Close()
	owner := &ranv1alpha1.TelcoHealthcheck{ObjectMeta: metav1.ObjectMeta{Name: ranv1alpha1.TelcoHealthcheckCanonicalName},
		Spec: ranv1alpha1.TelcoHealthcheckSpec{AlertManager: &ranv1alpha1.AlertManagerSpec{
			URL: server.URL, AuthType: ranv1alpha1.AlertManagerAuthBearer,
			CredentialsSecret: &ranv1alpha1.AlertManagerCredentialsSecret{Name: "outbound-alerts"},
		}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "outbound-alerts"},
		Data: map[string][]byte{"token": []byte("private-token")}}
	start := metav1.Now()
	record := &ranv1alpha1.TelcoHealthCheckRun{ObjectMeta: metav1.ObjectMeta{Namespace: operatorNamespace, Name: "run"},
		Status: ranv1alpha1.TelcoHealthCheckRunStatus{ClusterName: "spoke", AgenticRunName: "run",
			AlertNotifications: []ranv1alpha1.AlertNotification{{Firing: true, StartsAt: start}}}}
	hub := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithStatusSubresource(record).
		WithObjects(owner, secret, record).Build()
	r := &runAlertReconciler{Client: hub, reader: hub, namespace: operatorNamespace, http: server.Client()}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(record)}
	if _, err := r.Reconcile(ctx, request); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("expected sanitized authentication failure, got %v", err)
	}
	current := &ranv1alpha1.TelcoHealthCheckRun{}
	if err := hub.Get(ctx, request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.AlertNotifications) != 1 {
		t.Fatal("401 response discarded queued notification")
	}
	secret.Data["token"] = []byte("corrected-token")
	if err := hub.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := hub.Get(ctx, request.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if len(current.Status.AlertNotifications) != 0 {
		t.Fatal("notification was not delivered after Secret rotation")
	}
}
