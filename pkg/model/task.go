package model

import (
	"time"
)

// Task is a concrete, schedulable unit of work executed by a worker/executor.
// Each Task belongs to a StepInstance. Non-scatter steps produce 1 Task;
// scatter steps produce N Tasks (one per combination).
type Task struct {
	ID             string       `json:"id"`
	SubmissionID   string       `json:"submission_id"`
	StepID         string       `json:"step_id"`
	StepInstanceID string       `json:"step_instance_id,omitempty"`
	State          TaskState    `json:"state"`
	ExecutorType   ExecutorType `json:"executor_type"`
	ExternalID     string       `json:"external_id,omitempty"`
	BVBRCAppID     string       `json:"bvbrc_app_id,omitempty"`

	// ScatterIndex is the iteration index for scatter tasks (-1 for non-scatter).
	// Used to order outputs when merging scatter results.
	ScatterIndex int `json:"scatter_index"`

	// Tool contains the CWL CommandLineTool definition for worker execution.
	// Stored as raw JSON to avoid circular imports with pkg/cwl.
	// Workers parse this using the CWL parser.
	Tool map[string]any `json:"tool,omitempty"`

	// Job contains the resolved input values for this task execution.
	// This is separate from Inputs to distinguish resolved CWL inputs
	// from the legacy _base_command approach.
	Job map[string]any `json:"job,omitempty"`

	// RuntimeHints provides executor-specific runtime configuration.
	RuntimeHints *RuntimeHints `json:"runtime_hints,omitempty"`

	// Inputs contains legacy task inputs including reserved keys
	// (_base_command, _output_globs, _docker_image, _bvbrc_app_id).
	// Deprecated: Use Tool + Job for worker tasks.
	Inputs  map[string]any `json:"inputs,omitempty"`
	Outputs map[string]any `json:"outputs,omitempty"`

	// DependsOn is retained for backward compatibility with existing DB rows.
	// New code should use StepInstance-level dependency tracking instead.
	DependsOn   []string   `json:"depends_on,omitempty"`
	Priority    int        `json:"priority"`
	RetryCount  int        `json:"retry_count"`
	MaxRetries  int        `json:"max_retries"`
	Stdout      string     `json:"-"`
	Stderr      string     `json:"-"`
	ExitCode    *int       `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// DatasetRequirement describes a reference dataset needed by a CWL tool.
// Parsed from the gowe:ResourceData CWL hint.
type DatasetRequirement struct {
	ID     string `json:"id"`
	Path   string `json:"path,omitempty"`
	Size   string `json:"size,omitempty"`
	Mode   string `json:"mode"`            // "prestage" or "cache"
	Source string `json:"source,omitempty"` // Future: shock://..., s3://...
}

// RuntimeHints provides executor-specific runtime configuration.
type RuntimeHints struct {
	// DockerImage is the container image to use for Docker/container execution.
	DockerImage string `json:"docker_image,omitempty"`

	// Cores is the minimum number of CPU cores requested.
	Cores int `json:"cores,omitempty"`

	// RamMB is the minimum RAM in megabytes requested.
	RamMB int64 `json:"ram_mb,omitempty"`

	// ExpressionLib contains JavaScript library code from InlineJavascriptRequirement.
	ExpressionLib []string `json:"expression_lib,omitempty"`

	// Namespaces contains namespace prefix mappings for format resolution.
	Namespaces map[string]string `json:"namespaces,omitempty"`

	// StagerOverrides allows per-task stager customization.
	StagerOverrides *StagerOverrides `json:"stager_overrides,omitempty"`

	// WorkerGroup specifies which worker group should execute this task.
	// If empty, any worker can execute the task.
	WorkerGroup string `json:"worker_group,omitempty"`

	// CWLDir is the directory containing the CWL file, used for resolving relative paths.
	CWLDir string `json:"cwl_dir,omitempty"`

	// RequiresGPU indicates the task needs a GPU-enabled worker.
	RequiresGPU bool `json:"requires_gpu,omitempty"`

	// RequiredDatasets lists reference datasets needed by this task.
	// Parsed from the gowe:ResourceData CWL hint.
	RequiredDatasets []DatasetRequirement `json:"required_datasets,omitempty"`

	// OutputDestination is the target URI for uploading task outputs
	// (e.g., "ws:///user@bvbrc/home/results/"). Propagated from Submission.
	OutputDestination string `json:"output_destination,omitempty"`
}

// StagerOverrides allows per-task stager customization.
type StagerOverrides struct {
	// HTTPHeaders are additional headers for this task's HTTP requests.
	HTTPHeaders map[string]string `json:"http_headers,omitempty"`

	// HTTPTimeoutSeconds overrides the default HTTP timeout in seconds.
	HTTPTimeoutSeconds *int `json:"http_timeout_seconds,omitempty"`

	// HTTPCredential overrides credentials for this task.
	HTTPCredential *HTTPCredential `json:"http_credential,omitempty"`
}

// HTTPCredential holds authentication for HTTP requests.
type HTTPCredential struct {
	// Type specifies the authentication type: "bearer", "basic", or "header".
	Type string `json:"type"`

	// Token is the bearer token (for type="bearer").
	Token string `json:"token,omitempty"`

	// Username is the username for basic auth (for type="basic").
	Username string `json:"username,omitempty"`

	// Password is the password for basic auth (for type="basic").
	Password string `json:"password,omitempty"`

	// HeaderName is the custom header name (for type="header").
	HeaderName string `json:"header_name,omitempty"`

	// HeaderValue is the custom header value (for type="header").
	HeaderValue string `json:"header_value,omitempty"`
}

// HasTool returns true if this task has a Tool definition for worker execution.
func (t *Task) HasTool() bool {
	return t.Tool != nil && len(t.Tool) > 0
}
