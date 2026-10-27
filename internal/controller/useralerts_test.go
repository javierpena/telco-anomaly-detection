package controller

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
)

const testNamespace = "telco-healthcheck-system"

func makeUserAlertCM(name, namespace string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				userAlertManagedByLabel: userAlertManagedByValue,
				userAlertLabel:          userAlertLabelValue,
			},
		},
		Data: data,
	}
}

func TestListUserAlertConfigs_ValidEntry(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("my-alert", testNamespace, map[string]string{
		"alertName":    "MyAlert",
		"alertRule":    "- alert: MyAlert\n  expr: up == 0",
		"alertMetrics": `["my_metric_total", "another_metric"]`,
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	got := configs[0]
	if got.AlertName != "MyAlert" {
		t.Errorf("AlertName: got %q, want %q", got.AlertName, "MyAlert")
	}
	if got.ConfigMapName != "my-alert" {
		t.Errorf("ConfigMapName: got %q, want %q", got.ConfigMapName, "my-alert")
	}
	if len(got.AlertMetrics) != 2 {
		t.Errorf("AlertMetrics: got %v, want 2 entries", got.AlertMetrics)
	}
}

func TestListUserAlertConfigs_MissingAlertNameSkipped(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("bad-alert", testNamespace, map[string]string{
		"alertRule": "- alert: SomeAlert\n  expr: up == 0",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs (missing alertName), got %d", len(configs))
	}
}

func TestListUserAlertConfigs_MissingAlertRuleSkipped(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("bad-rule", testNamespace, map[string]string{
		"alertName": "SomeAlert",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs (missing alertRule), got %d", len(configs))
	}
}

func TestListUserAlertConfigs_UnlabeledCMIgnored(t *testing.T) {
	scheme := newTestScheme(t)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "no-labels", Namespace: testNamespace},
		Data: map[string]string{
			"alertName": "SomeAlert",
			"alertRule": "- alert: SomeAlert\n  expr: up == 0",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected 0 configs for unlabeled CM, got %d", len(configs))
	}
}

func TestListUserAlertConfigs_InvalidMetricsJSONSkipped(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("bad-metrics", testNamespace, map[string]string{
		"alertName":    "AlertBadMetrics",
		"alertRule":    "- alert: AlertBadMetrics\n  expr: up == 0",
		"alertMetrics": "not-valid-json",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("expected ConfigMap with invalid alertMetrics to be skipped, got %d configs", len(configs))
	}
}

func TestListUserAlertConfigs_EmptyMetricsParsed(t *testing.T) {
	scheme := newTestScheme(t)
	cm := makeUserAlertCM("no-metrics", testNamespace, map[string]string{
		"alertName": "AlertNoMetrics",
		"alertRule": "- alert: AlertNoMetrics\n  expr: up == 0",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

	configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if len(configs[0].AlertMetrics) != 0 {
		t.Errorf("expected no metrics, got %v", configs[0].AlertMetrics)
	}
}

func TestListUserAlertConfigs_UserWorkloadMetrics(t *testing.T) {
	tests := []struct {
		name            string
		raw             string
		platformMetrics string
		want            []string
		valid           bool
	}{
		{name: "omitted", valid: true},
		{name: "empty string", raw: "", valid: true},
		{name: "empty array", raw: "[]", valid: true},
		{name: "whitespace", raw: " \n [ ] \t", valid: true},
		{name: "workload only", raw: `["app_requests_total", "app_errors_total"]`, want: []string{"app_requests_total", "app_errors_total"}, valid: true},
		{name: "both lists", raw: `["app_requests_total"]`, platformMetrics: `["platform_metric"]`, want: []string{"app_requests_total"}, valid: true},
		{name: "malformed JSON", raw: "not-valid-json"},
		{name: "object", raw: `{}`},
		{name: "string", raw: `"app_requests_total"`},
		{name: "number", raw: `42`},
		{name: "boolean", raw: `true`},
		{name: "non-string entry", raw: `["app_requests_total", 42]`},
		{name: "null array", raw: `null`},
		{name: "null entry", raw: `["app_requests_total", null]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := map[string]string{
				"alertName": "MyAlert",
				"alertRule": "- alert: MyAlert\n  expr: up == 0",
			}
			if tt.name != "omitted" {
				data["alertUserWorkloadMetrics"] = tt.raw
			}
			if tt.platformMetrics != "" {
				data["alertMetrics"] = tt.platformMetrics
			}
			cm := makeUserAlertCM("my-alert", testNamespace, data)
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(cm).Build()

			configs, err := listUserAlertConfigs(context.Background(), c, testNamespace)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.valid {
				if len(configs) != 0 {
					t.Fatalf("expected invalid alert to be skipped, got %d configs", len(configs))
				}
				return
			}
			if len(configs) != 1 {
				t.Fatalf("expected 1 config, got %d", len(configs))
			}
			if got := configs[0].AlertUserWorkloadMetrics; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AlertUserWorkloadMetrics: got %v, want %v", got, tt.want)
			}
			var wantPlatform []string
			if tt.platformMetrics != "" {
				wantPlatform = []string{"platform_metric"}
			}
			if got := configs[0].AlertMetrics; !reflect.DeepEqual(got, wantPlatform) {
				t.Errorf("AlertMetrics: got %v, want %v", got, wantPlatform)
			}
		})
	}
}

func TestMapUserAlertCMToTHC_ReturnsSingletonRequest(t *testing.T) {
	scheme := newTestScheme(t)
	thc := &ranv1alpha1.TelcoHealthcheck{}
	thc.Name = ranv1alpha1.TelcoHealthcheckCanonicalName
	cm := makeUserAlertCM("some-alert", "some-other-namespace", map[string]string{
		"alertName": "SomeAlert",
		"alertRule": "- alert: SomeAlert\n  expr: up == 0",
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(thc, cm).Build()

	r := &TelcoHealthcheckReconciler{Client: c}
	requests := r.mapUserAlertCMToTHC(context.Background(), cm)

	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].Name != ranv1alpha1.TelcoHealthcheckCanonicalName {
		t.Errorf("expected canonical name %q, got %q", ranv1alpha1.TelcoHealthcheckCanonicalName, requests[0].Name)
	}
	if requests[0].Namespace != "" {
		t.Errorf("expected empty namespace for cluster-scoped CR, got %q", requests[0].Namespace)
	}
}
