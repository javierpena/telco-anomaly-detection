package alertreceiver

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ranv1alpha1 "github.com/javierpena/telco-anomaly-detection/api/v1alpha1"
	"github.com/javierpena/telco-anomaly-detection/internal/agenticrun"
)

func newHandlerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("corev1: %v", err)
	}
	if err := clusterv1.AddToScheme(s); err != nil {
		t.Fatalf("clusterv1: %v", err)
	}
	if err := ranv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("ranv1alpha1: %v", err)
	}
	return s
}

func makeTHCWithMonitoredClusters(name, namespace string, clusters []string) *ranv1alpha1.TelcoHealthcheck {
	thc := &ranv1alpha1.TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	thc.Status.MonitoredClusters = clusters
	return thc
}

func makeKubeconfigSecretUnstructured(clusterName string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-admin-kubeconfig",
			Namespace: clusterName,
		},
		Data: map[string][]byte{
			"kubeconfig": []byte("fake-kubeconfig"),
		},
	}
}

func makeAlertNamesConfigMap(alertNames []string) *unstructured.Unstructured {
	rules := "groups:\n"
	for _, name := range alertNames {
		rules += "  - name: group\n    rules:\n    - alert: " + name + "\n      expr: up == 0\n"
	}
	cm := &unstructured.Unstructured{}
	cm.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
	cm.SetName(thanosRulerConfigMap)
	cm.SetNamespace(observabilityNamespace)
	if err := unstructured.SetNestedStringMap(cm.Object, map[string]string{
		customRulesKey: rules,
	}, "data"); err != nil {
		panic(err)
	}
	return cm
}

func TestExtractAlertName(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"    - alert: MyAlert", "MyAlert"},
		{"    - alert:   SpacedAlert", "SpacedAlert"},
		{"    record: some_metric", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractAlertName(tt.line)
		if got != tt.want {
			t.Errorf("extractAlertName(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestParseAlertNamesFromRulesYAML(t *testing.T) {
	input := `groups:
  - name: test
    rules:
    - alert: AlertOne
      expr: up == 0
    - alert: AlertTwo
      expr: up == 0
`
	names := parseAlertNamesFromRulesYAML(input)
	if !names["AlertOne"] {
		t.Error("expected AlertOne")
	}
	if !names["AlertTwo"] {
		t.Error("expected AlertTwo")
	}
}

func makeAgenticRunConfigMapUnstructured(name, namespace string) *unstructured.Unstructured {
	cm := &unstructured.Unstructured{}
	cm.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"})
	cm.SetName(name)
	cm.SetNamespace(namespace)
	if err := unstructured.SetNestedStringMap(cm.Object, map[string]string{
		"request":    "test-request",
		"skills":     `[{"image":"quay.io/test:latest","paths":[]}]`,
		"mcpServers": "[]",
	}, "data"); err != nil {
		panic(err)
	}
	return cm
}

func TestProcessAlerts_MatchCreatesAgenticRun(t *testing.T) {
	scheme := newHandlerScheme(t)

	const alertName = "TelcoHealthCheckHostNetwork"

	thc := makeTHCWithMonitoredClusters("test", "default", []string{"cluster-a"})
	kubeSecret := makeKubeconfigSecretUnstructured("cluster-a")
	alertCM := makeAlertNamesConfigMap([]string{alertName})
	configCM := makeAgenticRunConfigMapUnstructured("telco-anomaly-host-network-config", operatorNamespace)

	hubClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(thc).
		WithObjects(thc, kubeSecret, alertCM, configCM).
		Build()

	spokeFake := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

	h := &Handler{
		HubClient: hubClient,
		NewSpokeClient: func(_ []byte) (client.Client, error) {
			return spokeFake, nil
		},
	}

	alerts := []Alert{
		{
			Status:   "firing",
			Labels:   map[string]string{"cluster": "cluster-a", "alertname": alertName},
			StartsAt: time.Now(),
		},
	}
	if err := h.processAlerts(context.Background(), alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runList := &unstructured.UnstructuredList{}
	runList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   agenticrun.Group,
		Version: agenticrun.Version,
		Kind:    agenticrun.Kind + "List",
	})
	if err := spokeFake.List(context.Background(), runList); err != nil {
		t.Fatalf("listing AgenticRuns: %v", err)
	}
	if len(runList.Items) == 0 {
		t.Error("expected at least one AgenticRun to be created")
	}
}

func TestProcessAlerts_NonFiringAlertSkipped(t *testing.T) {
	scheme := newHandlerScheme(t)

	const alertName = "TelcoHealthCheckHostNetwork"

	thc := makeTHCWithMonitoredClusters("test", "default", []string{"cluster-a"})
	kubeSecret := makeKubeconfigSecretUnstructured("cluster-a")
	alertCM := makeAlertNamesConfigMap([]string{alertName})
	configCM := makeAgenticRunConfigMapUnstructured("telco-anomaly-host-network-config", operatorNamespace)

	hubClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(thc).
		WithObjects(thc, kubeSecret, alertCM, configCM).
		Build()

	spokeFake := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
	h := &Handler{
		HubClient:      hubClient,
		NewSpokeClient: func(_ []byte) (client.Client, error) { return spokeFake, nil },
	}

	alerts := []Alert{
		{
			Status: "resolved",
			Labels: map[string]string{"cluster": "cluster-a", "alertname": alertName},
		},
	}
	if err := h.processAlerts(context.Background(), alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runList := &unstructured.UnstructuredList{}
	runList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   agenticrun.Group,
		Version: agenticrun.Version,
		Kind:    agenticrun.Kind + "List",
	})
	_ = spokeFake.List(context.Background(), runList)
	if len(runList.Items) != 0 {
		t.Errorf("expected no AgenticRuns for resolved alert, got %d", len(runList.Items))
	}
}

func TestProcessAlerts_UnmonitoredClusterSkipped(t *testing.T) {
	scheme := newHandlerScheme(t)

	thc := makeTHCWithMonitoredClusters("test", "default", []string{"cluster-a"})
	alertCM := makeAlertNamesConfigMap([]string{"TargetAlert"})
	hubClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(thc).
		WithObjects(thc, alertCM).
		Build()

	spokeFake := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
	h := &Handler{
		HubClient:      hubClient,
		NewSpokeClient: func(_ []byte) (client.Client, error) { return spokeFake, nil },
	}

	alerts := []Alert{
		{Labels: map[string]string{"cluster": "unknown-cluster", "alertname": "TargetAlert"}},
	}
	if err := h.processAlerts(context.Background(), alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runList := &unstructured.UnstructuredList{}
	runList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   agenticrun.Group,
		Version: agenticrun.Version,
		Kind:    agenticrun.Kind + "List",
	})
	_ = spokeFake.List(context.Background(), runList)
	if len(runList.Items) != 0 {
		t.Errorf("expected no AgenticRuns created for unmonitored cluster, got %d", len(runList.Items))
	}
}

func TestProcessAlerts_UndefinedAlertSkipped(t *testing.T) {
	scheme := newHandlerScheme(t)

	thc := makeTHCWithMonitoredClusters("test", "default", []string{"cluster-a"})
	kubeSecret := makeKubeconfigSecretUnstructured("cluster-a")
	alertCM := makeAlertNamesConfigMap([]string{"DefinedAlert"})
	hubClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(thc).
		WithObjects(thc, kubeSecret, alertCM).
		Build()

	spokeFake := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
	h := &Handler{
		HubClient:      hubClient,
		NewSpokeClient: func(_ []byte) (client.Client, error) { return spokeFake, nil },
	}

	alerts := []Alert{
		{Labels: map[string]string{"cluster": "cluster-a", "alertname": "UndefinedAlert"}},
	}
	if err := h.processAlerts(context.Background(), alerts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runList := &unstructured.UnstructuredList{}
	runList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   agenticrun.Group,
		Version: agenticrun.Version,
		Kind:    agenticrun.Kind + "List",
	})
	_ = spokeFake.List(context.Background(), runList)
	if len(runList.Items) != 0 {
		t.Errorf("expected no AgenticRuns created for undefined alert, got %d", len(runList.Items))
	}
}
