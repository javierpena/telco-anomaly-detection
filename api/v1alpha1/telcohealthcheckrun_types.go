package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TriggerType identifies what started a run.
// +kubebuilder:validation:Enum=alert;periodicHealthCheck
type TriggerType string

const (
	TriggerTypeAlert               TriggerType = "alert"
	TriggerTypePeriodicHealthCheck TriggerType = "periodicHealthCheck"
)

// AgenticRunStatus tracks spoke creation until an AnalysisResult is observed,
// then contains its latest condition and conclusion.
type AgenticRunStatus struct {
	// Phase is Pending during spoke creation, Created or Failed once its outcome
	// is known, then the reason of the latest AnalysisResult condition.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Summary is taken from the result diagnosis, an option, or its failure reason.
	// +optional
	Summary string `json:"summary,omitempty"`
}

// TelcoHealthCheckRunSpec is empty; this record is managed by the operator.
type TelcoHealthCheckRunSpec struct{}

// TelcoHealthCheckRunStatus identifies the spoke run and records its observed result.
type TelcoHealthCheckRunStatus struct {
	AgenticRunName string      `json:"agenticRunName"`
	ClusterName    string      `json:"clusterName"`
	TriggeredBy    TriggerType `json:"triggeredBy"`
	Trigger        string      `json:"trigger"`
	// +optional
	AgenticRunStatus *AgenticRunStatus `json:"agenticRunStatus,omitempty"`
	// AgenticRunActionRequired mirrors AnalysisResult.status.actionRequired.
	// It is absent until the AnalysisResult reports "True" or "False".
	// +optional
	// +kubebuilder:validation:Enum=True;False
	AgenticRunActionRequired string `json:"agenticRunActionRequired,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=thcr
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.status.clusterName`
// +kubebuilder:printcolumn:name="Triggered By",type=string,JSONPath=`.status.triggeredBy`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.status.trigger`
// +kubebuilder:printcolumn:name="AgenticRun",type=string,JSONPath=`.status.agenticRunName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.agenticRunStatus.phase`
// +kubebuilder:printcolumn:name="Action Required",type=string,JSONPath=`.status.agenticRunActionRequired`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// TelcoHealthCheckRun is a hub-side audit record of a spoke AgenticRun.
type TelcoHealthCheckRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TelcoHealthCheckRunSpec   `json:"spec,omitempty"`
	Status            TelcoHealthCheckRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// TelcoHealthCheckRunList contains hub-side run records.
type TelcoHealthCheckRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TelcoHealthCheckRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TelcoHealthCheckRun{}, &TelcoHealthCheckRunList{})
}
