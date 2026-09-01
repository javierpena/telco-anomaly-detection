package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTelcoHealthcheckDeepCopy(t *testing.T) {
	period := metav1.Duration{Duration: 5 * time.Minute}
	rdsPeriod := metav1.Duration{Duration: 10 * time.Minute}

	original := &TelcoHealthcheck{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
		Spec: TelcoHealthcheckSpec{
			ManagedClusters: ManagedClustersSpec{
				Include: []string{"cluster-a", "cluster-b"},
			},
			ManagedNamespaces: []string{"ns-1", "ns-2"},
			Alerts: AlertsSpec{
				HostNetwork: true,
				PodNetwork:  false,
			},
			PeriodicHealthChecks: PeriodicHealthChecksSpec{
				Period: period,
				RDSCompliance: RDSComplianceSpec{
					Period:  &rdsPeriod,
					Enabled: true,
				},
			},
		},
	}

	copy := original.DeepCopy()

	if copy.Name != original.Name {
		t.Errorf("expected name %q, got %q", original.Name, copy.Name)
	}
	if len(copy.Spec.ManagedClusters.Include) != len(original.Spec.ManagedClusters.Include) {
		t.Errorf("include list length mismatch: expected %d, got %d",
			len(original.Spec.ManagedClusters.Include), len(copy.Spec.ManagedClusters.Include))
	}

	// Verify the copy is independent
	copy.Spec.ManagedClusters.Include[0] = "modified"
	if original.Spec.ManagedClusters.Include[0] == "modified" {
		t.Error("DeepCopy did not produce an independent copy of Include slice")
	}

	copy.Spec.ManagedNamespaces[0] = "modified-ns"
	if original.Spec.ManagedNamespaces[0] == "modified-ns" {
		t.Error("DeepCopy did not produce an independent copy of ManagedNamespaces slice")
	}

	if copy.Spec.PeriodicHealthChecks.RDSCompliance.Period == original.Spec.PeriodicHealthChecks.RDSCompliance.Period {
		t.Error("DeepCopy did not produce an independent copy of RDSCompliance.Period pointer")
	}
}

func TestManagedClustersSpecDeepCopy(t *testing.T) {
	original := &ManagedClustersSpec{
		Include: []string{"a", "b"},
		Exclude: []string{"c"},
	}
	copy := original.DeepCopy()

	if copy == nil {
		t.Fatal("DeepCopy returned nil")
	}
	if len(copy.Include) != 2 || len(copy.Exclude) != 1 {
		t.Errorf("unexpected copy content: include=%v exclude=%v", copy.Include, copy.Exclude)
	}
}

func TestTelcoHealthcheckListDeepCopy(t *testing.T) {
	list := &TelcoHealthcheckList{
		Items: []TelcoHealthcheck{
			{ObjectMeta: metav1.ObjectMeta{Name: "item-1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "item-2"}},
		},
	}
	copy := list.DeepCopy()

	if len(copy.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(copy.Items))
	}
	copy.Items[0].Name = "modified"
	if list.Items[0].Name == "modified" {
		t.Error("DeepCopy of list did not produce independent item copies")
	}
}

func TestGroupVersion(t *testing.T) {
	if GroupVersion.Group != "ran.openshift.io" {
		t.Errorf("unexpected group: %q", GroupVersion.Group)
	}
	if GroupVersion.Version != "v1alpha1" {
		t.Errorf("unexpected version: %q", GroupVersion.Version)
	}
}
