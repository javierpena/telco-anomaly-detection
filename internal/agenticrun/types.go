// Package agenticrun provides type definitions for the AgenticRun CRD
// (agentic.openshift.io/v1alpha1). These types are defined locally since
// the lightspeed-agentic-operator does not yet publish a Go client library.
// They are used as documentation and for building unstructured objects; the
// actual Kubernetes objects are created via the dynamic/unstructured client.
package agenticrun

const (
	// Group is the API group for AgenticRun resources.
	Group = "agentic.openshift.io"
	// Version is the API version for AgenticRun resources.
	Version = "v1alpha1"
	// Kind is the kind name for AgenticRun resources.
	Kind = "AgenticRun"
	// APIVersion is the fully qualified API version string.
	APIVersion = Group + "/" + Version
	// Namespace is the namespace where AgenticRun resources are always created on spoke clusters.
	Namespace = "openshift-lightspeed"
)

// SkillSpec references an OCI image containing agent skill files.
type SkillSpec struct {
	// Image is the OCI image reference for the skills container.
	// Must include a domain, repository path, and tag or digest.
	Image string `json:"image"`
	// Paths lists absolute file paths within the image that contain skill definitions.
	// Each path must be absolute, use only [a-zA-Z0-9/_.-], and contain no "..".
	Paths []string `json:"paths,omitempty"`
}

// MCPServerSpec configures an MCP server accessible to the agent.
type MCPServerSpec struct {
	// Name is a DNS-label identifier for the MCP server (^[a-z][a-z0-9-]*$).
	Name string `json:"name"`
	// URL is the HTTP/HTTPS endpoint for the MCP server.
	URL string `json:"url"`
	// TimeoutSeconds is the per-request timeout. Defaults to 5.
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// ToolsSpec bundles all tools available to an agentic run.
type ToolsSpec struct {
	// Skills is a list of OCI image references providing skill files.
	// +optional
	Skills []SkillSpec `json:"skills,omitempty"`
	// MCPServers lists MCP servers the agent can call.
	// +optional
	MCPServers []MCPServerSpec `json:"mcpServers,omitempty"`
}

// AnalysisSpec configures the analysis step of an AgenticRun.
// Required by the CRD (minProperties: 1).
type AnalysisSpec struct {
	// Agent is the DNS subdomain name of the agent to use. Defaults to "default".
	// +optional
	Agent string `json:"agent,omitempty"`
	// Tools provides the toolset available for the analysis step.
	// +optional
	Tools *ToolsSpec `json:"tools,omitempty"`
}

// AgenticRunSpec defines the desired execution of an agentic run.
// All fields except revisionFeedback and ttlAfterTerminal are immutable after creation.
type AgenticRunSpec struct {
	// Request is the user's original prompt or alert description (1–32768 chars).
	// Immutable after creation.
	Request string `json:"request"`
	// Analysis is required by the CRD. At minimum, agent defaults to "default".
	Analysis *AnalysisSpec `json:"analysis"`
	// Tools provides the shared toolset available across all analysis steps.
	// +optional
	Tools *ToolsSpec `json:"tools,omitempty"`
}

// AgenticRunPhase represents the lifecycle phase of an AgenticRun.
type AgenticRunPhase string

const (
	AgenticRunPhasePending   AgenticRunPhase = "Pending"
	AgenticRunPhaseRunning   AgenticRunPhase = "Running"
	AgenticRunPhaseSucceeded AgenticRunPhase = "Succeeded"
	AgenticRunPhaseFailed    AgenticRunPhase = "Failed"
)

// AgenticRunStatus describes the current state of an AgenticRun.
type AgenticRunStatus struct {
	// Phase is the high-level lifecycle phase.
	Phase AgenticRunPhase `json:"phase,omitempty"`
}
