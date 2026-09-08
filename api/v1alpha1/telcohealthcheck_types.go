package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LogLevel controls operator logging verbosity.
// +kubebuilder:validation:Enum=info;debug
type LogLevel string

const (
	LogLevelInfo  LogLevel = "info"
	LogLevelDebug LogLevel = "debug"
)

// IsDebugLevel returns true if any CR in items requests debug logging.
func IsDebugLevel(items []TelcoHealthcheck) bool {
	for _, thc := range items {
		if thc.Spec.LogLevel == LogLevelDebug {
			return true
		}
	}
	return false
}

// ManagedClustersSpec selects which ManagedCluster resources to monitor.
// Exactly one of Include or Exclude may be specified.
// +kubebuilder:validation:XValidation:rule="!(has(self.include) && self.include.size() > 0 && has(self.exclude) && self.exclude.size() > 0)",message="include and exclude are mutually exclusive"
type ManagedClustersSpec struct {
	// Include lists ManagedCluster names to monitor. All others are ignored.
	// +optional
	Include []string `json:"include,omitempty"`
	// Exclude lists ManagedCluster names to skip. All others are monitored.
	// +optional
	Exclude []string `json:"exclude,omitempty"`
}

// AlertsSpec controls which categories of alerts are forwarded to the alert receiver.
type AlertsSpec struct {
	// HostNetwork enables monitoring of host-network related alerts.
	HostNetwork bool `json:"hostNetwork"`
	// PodNetwork enables monitoring of pod-network related alerts.
	PodNetwork bool `json:"podNetwork"`
	// HostReservedCPU enables monitoring of host reserved-CPU related alerts.
	HostReservedCPU bool `json:"hostReservedCPU"`
	// OVSProcessCPU enables monitoring of OVS process CPU usage alerts.
	OVSProcessCPU bool `json:"ovsProcessCPU"`
}

// RDSComplianceSpec configures periodic RDS compliance health checks.
type RDSComplianceSpec struct {
	// Period overrides the default check interval for RDS compliance checks.
	// +optional
	Period *metav1.Duration `json:"period,omitempty"`
	// Enabled activates RDS compliance checks when true.
	Enabled bool `json:"enabled"`
}

// PeriodicHealthChecksSpec defines the schedule for periodic agentic health checks.
type PeriodicHealthChecksSpec struct {
	// Period is the default interval between periodic health checks across all clusters.
	Period metav1.Duration `json:"period"`
	// RDSCompliance configures the RDS compliance check schedule and enablement.
	// +optional
	RDSCompliance RDSComplianceSpec `json:"rdsCompliance,omitempty"`
}

// TelcoHealthcheckSpec defines the desired state of TelcoHealthcheck.
type TelcoHealthcheckSpec struct {
	// ManagedClusters selects which ACM ManagedCluster resources are monitored.
	ManagedClusters ManagedClustersSpec `json:"managedClusters"`
	// ManagedNamespaces lists the namespaces to monitor on each managed cluster.
	// An empty list means no namespace monitoring is active.
	ManagedNamespaces []string `json:"managedNamespaces"`
	// Alerts controls which alert categories create Thanos rules and trigger agentic runs.
	Alerts AlertsSpec `json:"alerts"`
	// PeriodicHealthChecks defines the schedule for periodic agentic health checks.
	PeriodicHealthChecks PeriodicHealthChecksSpec `json:"periodicHealthChecks"`
	// LogLevel controls logging verbosity for both the controller and alert receiver.
	// Set to "debug" to enable V(1) messages. Changes take effect on the next
	// reconcile without restarting pods. Defaults to "info".
	// +optional
	// +kubebuilder:default=info
	LogLevel LogLevel `json:"logLevel,omitempty"`
}

// TelcoHealthcheckStatus defines the observed state of TelcoHealthcheck.
type TelcoHealthcheckStatus struct {
	// Conditions reports the current reconciliation state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// MonitoredClusters is the resolved list of cluster names currently being monitored.
	// +optional
	MonitoredClusters []string `json:"monitoredClusters,omitempty"`
	// LastPeriodicRunTime records when the last periodic health check was triggered.
	// +optional
	LastPeriodicRunTime *metav1.Time `json:"lastPeriodicRunTime,omitempty"`
	// LastRDSComplianceRunTime records when the last RDS compliance check was triggered.
	// +optional
	LastRDSComplianceRunTime *metav1.Time `json:"lastRDSComplianceRunTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=thc
// +kubebuilder:printcolumn:name="Clusters",type=integer,JSONPath=`.status.monitoredClusters`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// TelcoHealthcheck monitors telco workload health across ACM managed clusters.
type TelcoHealthcheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TelcoHealthcheckSpec   `json:"spec,omitempty"`
	Status TelcoHealthcheckStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// TelcoHealthcheckList contains a list of TelcoHealthcheck.
type TelcoHealthcheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TelcoHealthcheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TelcoHealthcheck{}, &TelcoHealthcheckList{})
}
