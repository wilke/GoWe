package bvbrc

import (
	"encoding/json"
	"time"
)

// TaskState represents the current state of a job/task.
type TaskState string

const (
	TaskStateQueued     TaskState = "queued"
	TaskStateInProgress TaskState = "in-progress"
	TaskStateCompleted  TaskState = "completed"
	TaskStateFailed     TaskState = "failed"
	TaskStateDeleted    TaskState = "deleted"
	TaskStateSuspended  TaskState = "suspended"
)

// IsTerminal returns true if the state is a final state.
func (s TaskState) IsTerminal() bool {
	switch s {
	case TaskStateCompleted, TaskStateFailed, TaskStateDeleted:
		return true
	default:
		return false
	}
}

// Task represents a submitted job/task in the App Service.
type Task struct {
	// ID is the unique task identifier (UUID).
	ID string `json:"id"`

	// App is the application identifier that was run.
	App string `json:"app"`

	// Owner is the username of the task owner.
	Owner string `json:"owner"`

	// Status is the current execution state.
	Status TaskState `json:"status"`

	// SubmitTime is when the task was submitted.
	SubmitTime *time.Time `json:"submit_time,omitempty"`

	// StartTime is when execution began.
	StartTime *time.Time `json:"start_time,omitempty"`

	// CompletedTime is when execution finished.
	CompletedTime *time.Time `json:"completed_time,omitempty"`

	// Parameters contains the application-specific parameters used.
	Parameters map[string]any `json:"parameters,omitempty"`

	// OutputPath is the workspace path where results are stored.
	OutputPath string `json:"output_path,omitempty"`

	// ClusterJobID is the backend cluster job identifier.
	ClusterJobID string `json:"cluster_job_id,omitempty"`

	// Hostname is the compute node where the job ran.
	Hostname string `json:"hostname,omitempty"`

	// StdoutURL is the URL to the job's stdout log.
	StdoutURL string `json:"stdout_url,omitempty"`

	// StderrURL is the URL to the job's stderr log.
	StderrURL string `json:"stderr_url,omitempty"`
}

// TaskSummary holds aggregated task counts by status.
type TaskSummary struct {
	Queued     int `json:"queued"`
	InProgress int `json:"in-progress"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Deleted    int `json:"deleted"`
}

// AppDescription describes an available bioinformatics application.
type AppDescription struct {
	// ID is the unique application identifier.
	ID string `json:"id"`

	// Label is the human-readable application name.
	Label string `json:"label"`

	// Description provides a brief description of what the app does.
	Description string `json:"description"`

	// Parameters defines the app's parameter schema.
	Parameters []AppParameter `json:"parameters,omitempty"`

	// DefaultMemory is the default memory allocation.
	DefaultMemory string `json:"default_memory,omitempty"`

	// DefaultCPU is the default CPU allocation.
	DefaultCPU int `json:"default_cpu,omitempty"`
}

// AppParameter describes a single parameter for an application.
type AppParameter struct {
	// ID is the parameter identifier.
	ID string `json:"id"`

	// Label is the human-readable parameter name.
	Label string `json:"label"`

	// Type is the parameter data type.
	Type string `json:"type"`

	// Required indicates whether the parameter is mandatory.
	Required bool `json:"required"`

	// Default is the default value if not specified.
	Default any `json:"default,omitempty"`

	// Description explains the parameter.
	Description string `json:"desc,omitempty"`

	// EnumValues lists valid values for enum-type parameters.
	EnumValues []string `json:"enum,omitempty"`
}

// WorkspaceObjectType represents the type of a workspace object.
type WorkspaceObjectType string

const (
	WorkspaceTypeFolder              WorkspaceObjectType = "folder"
	WorkspaceTypeModelFolder         WorkspaceObjectType = "modelfolder"
	WorkspaceTypeJobResult           WorkspaceObjectType = "job_result"
	WorkspaceTypeContigs             WorkspaceObjectType = "contigs"
	WorkspaceTypeReads               WorkspaceObjectType = "reads"
	WorkspaceTypeFeatureGroup        WorkspaceObjectType = "feature_group"
	WorkspaceTypeGenomeGroup         WorkspaceObjectType = "genome_group"
	WorkspaceTypeExperimentGroup     WorkspaceObjectType = "experiment_group"
	WorkspaceTypeUnspecified         WorkspaceObjectType = "unspecified"
	WorkspaceTypeDiffExpInputData    WorkspaceObjectType = "diffexp_input_data"
	WorkspaceTypeDiffExpInputMeta    WorkspaceObjectType = "diffexp_input_metadata"
	WorkspaceTypeHTML                WorkspaceObjectType = "html"
	WorkspaceTypePDF                 WorkspaceObjectType = "pdf"
	WorkspaceTypeTxt                 WorkspaceObjectType = "txt"
	WorkspaceTypeJSON                WorkspaceObjectType = "json"
	WorkspaceTypeCSV                 WorkspaceObjectType = "csv"
	WorkspaceTypeNewick              WorkspaceObjectType = "nwk"
	WorkspaceTypeSVG                 WorkspaceObjectType = "svg"
)

// WorkspaceObject represents a file or folder in the BV-BRC workspace.
type WorkspaceObject struct {
	// Path is the full workspace path.
	Path string `json:"path"`

	// Type is the object type.
	Type WorkspaceObjectType `json:"type"`

	// Owner is the object owner's username.
	Owner string `json:"owner"`

	// CreationTime is when the object was created.
	CreationTime time.Time `json:"creation_time"`

	// ID is the unique object identifier (UUID).
	ID string `json:"id"`

	// OwnerID is the owner's identifier.
	OwnerID string `json:"owner_id"`

	// Size is the file size in bytes.
	Size int64 `json:"size"`

	// UserMetadata contains user-defined metadata.
	UserMetadata map[string]string `json:"user_metadata"`

	// AutoMetadata contains system-generated metadata.
	AutoMetadata map[string]string `json:"auto_metadata"`

	// ShockRef indicates storage type ("shock" or "inline").
	ShockRef string `json:"shock_ref,omitempty"`

	// ShockNodeID is the Shock node identifier for large files.
	ShockNodeID string `json:"shock_node_id,omitempty"`

	// Data contains inline file content (for small files).
	Data string `json:"data,omitempty"`
}

// WorkspacePermission represents a permission entry.
type WorkspacePermission struct {
	// Username is the user being granted permission.
	Username string `json:"username"`

	// Permission is the level: "r" (read), "w" (write), "o" (owner), "n" (none).
	Permission string `json:"permission"`
}

// RPCRequest represents a JSON-RPC 1.1 request envelope.
type RPCRequest struct {
	// ID is a unique identifier for the request.
	ID string `json:"id"`

	// Method is the fully-qualified method name.
	Method string `json:"method"`

	// Version is the JSON-RPC protocol version (always "1.1").
	Version string `json:"version"`

	// Params contains the method parameters as a positional array.
	Params []any `json:"params"`
}

// RPCResponse represents a JSON-RPC 1.1 response envelope.
type RPCResponse struct {
	// ID matches the request ID.
	ID string `json:"id"`

	// Version is the JSON-RPC protocol version.
	Version string `json:"version"`

	// Result contains the successful response data.
	Result json.RawMessage `json:"result,omitempty"`

	// Error contains error information if the call failed.
	Error *RPCError `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error object.
type RPCError struct {
	// Name is the error class name.
	Name string `json:"name"`

	// Code is the numeric error code.
	Code int `json:"code"`

	// Message is a human-readable error description.
	Message string `json:"message"`

	// Data contains additional error data.
	Data any `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *RPCError) Error() string {
	return e.Message
}

// AuthToken represents a parsed BV-BRC authentication token.
type AuthToken struct {
	// Raw is the complete token string.
	Raw string `json:"-"`

	// Username is the authenticated user's login name.
	Username string `json:"username"`

	// TokenID is the unique identifier for this token.
	TokenID string `json:"token_id"`

	// Expiry is when the token expires.
	Expiry time.Time `json:"expiry"`

	// ClientID identifies the client application.
	ClientID string `json:"client_id"`

	// Signature is the cryptographic signature.
	Signature string `json:"signature"`
}

// IsExpired returns true if the token has expired.
func (t *AuthToken) IsExpired() bool {
	return time.Now().After(t.Expiry)
}
