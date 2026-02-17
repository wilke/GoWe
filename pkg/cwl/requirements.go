package cwl

// Requirements holds all CWL requirement types that can appear in requirements or hints.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#Requirements_and_hints
type Requirements struct {
	// Docker specifies container execution requirements.
	Docker *DockerRequirement `json:"DockerRequirement,omitempty"`

	// Resource specifies compute resource requirements.
	Resource *ResourceRequirement `json:"ResourceRequirement,omitempty"`

	// InitialWorkDir specifies files to stage in the working directory.
	InitialWorkDir *InitialWorkDirRequirement `json:"InitialWorkDirRequirement,omitempty"`

	// EnvVar specifies environment variables to set.
	EnvVar *EnvVarRequirement `json:"EnvVarRequirement,omitempty"`

	// ShellCommand enables shell command interpretation.
	ShellCommand *ShellCommandRequirement `json:"ShellCommandRequirement,omitempty"`

	// InlineJavascript enables JavaScript expressions.
	InlineJavascript *InlineJavascriptRequirement `json:"InlineJavascriptRequirement,omitempty"`

	// SchemaDefRequirement defines custom types for this tool/workflow.
	SchemaDef *SchemaDefRequirement `json:"SchemaDefRequirement,omitempty"`

	// SoftwareRequirement specifies software dependencies.
	Software *SoftwareRequirement `json:"SoftwareRequirement,omitempty"`

	// NetworkAccess indicates if network access is required.
	NetworkAccess *NetworkAccessRequirement `json:"NetworkAccessRequirement,omitempty"`

	// WorkReuse controls whether the tool can reuse previous outputs.
	WorkReuse *WorkReuseRequirement `json:"WorkReuseRequirement,omitempty"`

	// ToolTimeLimit specifies maximum execution time.
	ToolTimeLimit *ToolTimeLimitRequirement `json:"ToolTimeLimitRequirement,omitempty"`

	// SubworkflowFeature enables subworkflows.
	SubworkflowFeature *SubworkflowFeatureRequirement `json:"SubworkflowFeatureRequirement,omitempty"`

	// ScatterFeature enables scatter operations.
	ScatterFeature *ScatterFeatureRequirement `json:"ScatterFeatureRequirement,omitempty"`

	// MultipleInputFeature enables merge_flattened and merge_nested.
	MultipleInputFeature *MultipleInputFeatureRequirement `json:"MultipleInputFeatureRequirement,omitempty"`

	// StepInputExpression enables valueFrom expressions on step inputs.
	StepInputExpression *StepInputExpressionRequirement `json:"StepInputExpressionRequirement,omitempty"`

	// InplaceUpdate allows in-place file modification.
	InplaceUpdate *InplaceUpdateRequirement `json:"InplaceUpdateRequirement,omitempty"`

	// LoadListing controls directory listing behavior.
	LoadListing *LoadListingRequirement `json:"LoadListingRequirement,omitempty"`
}

// DockerRequirement specifies container execution.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#DockerRequirement
type DockerRequirement struct {
	Class string `json:"class,omitempty"` // "DockerRequirement"

	// DockerPull specifies the Docker image to pull (e.g., "ubuntu:20.04").
	DockerPull string `json:"dockerPull,omitempty"`

	// DockerLoad specifies a URL to a Docker image tarball to load.
	DockerLoad string `json:"dockerLoad,omitempty"`

	// DockerFile specifies the contents of a Dockerfile to build.
	DockerFile string `json:"dockerFile,omitempty"`

	// DockerImport specifies a URL to a tarball to import as a Docker image.
	DockerImport string `json:"dockerImport,omitempty"`

	// DockerImageId specifies the image ID to use (after pull/load/build/import).
	DockerImageId string `json:"dockerImageId,omitempty"`

	// DockerOutputDirectory specifies the output directory inside the container.
	// Default is a platform-specific location (typically /var/spool/cwl or similar).
	DockerOutputDirectory string `json:"dockerOutputDirectory,omitempty"`
}

// ResourceRequirement specifies compute resource requirements.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#ResourceRequirement
type ResourceRequirement struct {
	Class string `json:"class,omitempty"` // "ResourceRequirement"

	// CoresMin is the minimum number of CPU cores required.
	CoresMin any `json:"coresMin,omitempty"` // int, float, or expression

	// CoresMax is the maximum number of CPU cores to allocate.
	CoresMax any `json:"coresMax,omitempty"`

	// RamMin is the minimum RAM in mebibytes (MiB).
	RamMin any `json:"ramMin,omitempty"`

	// RamMax is the maximum RAM in mebibytes (MiB).
	RamMax any `json:"ramMax,omitempty"`

	// TmpdirMin is the minimum temp directory space in mebibytes.
	TmpdirMin any `json:"tmpdirMin,omitempty"`

	// TmpdirMax is the maximum temp directory space in mebibytes.
	TmpdirMax any `json:"tmpdirMax,omitempty"`

	// OutdirMin is the minimum output directory space in mebibytes.
	OutdirMin any `json:"outdirMin,omitempty"`

	// OutdirMax is the maximum output directory space in mebibytes.
	OutdirMax any `json:"outdirMax,omitempty"`
}

// InitialWorkDirRequirement specifies files to stage in the working directory.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#InitialWorkDirRequirement
type InitialWorkDirRequirement struct {
	Class string `json:"class,omitempty"` // "InitialWorkDirRequirement"

	// Listing is the list of files/directories to stage.
	// Can be a list of Dirents, File/Directory objects, expressions, or strings.
	Listing any `json:"listing"`
}

// EnvVarRequirement specifies environment variables.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#EnvVarRequirement
type EnvVarRequirement struct {
	Class string `json:"class,omitempty"` // "EnvVarRequirement"

	// EnvDef is the list of environment variable definitions.
	EnvDef []EnvironmentDef `json:"envDef"`
}

// ShellCommandRequirement enables shell interpretation of the command.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#ShellCommandRequirement
type ShellCommandRequirement struct {
	Class string `json:"class,omitempty"` // "ShellCommandRequirement"
}

// InlineJavascriptRequirement enables JavaScript expressions.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#InlineJavascriptRequirement
type InlineJavascriptRequirement struct {
	Class string `json:"class,omitempty"` // "InlineJavascriptRequirement"

	// ExpressionLib is an array of JavaScript code to include before expressions.
	ExpressionLib []string `json:"expressionLib,omitempty"`
}

// SchemaDefRequirement defines custom record types.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#SchemaDefRequirement
type SchemaDefRequirement struct {
	Class string `json:"class,omitempty"` // "SchemaDefRequirement"

	// Types is the list of type definitions.
	Types []any `json:"types"`
}

// SoftwareRequirement specifies software dependencies.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#SoftwareRequirement
type SoftwareRequirement struct {
	Class string `json:"class,omitempty"` // "SoftwareRequirement"

	// Packages is the list of software packages required.
	Packages []SoftwarePackage `json:"packages"`
}

// SoftwarePackage describes a software dependency.
type SoftwarePackage struct {
	// Package is the name of the software package.
	Package string `json:"package"`

	// Version is a list of acceptable versions (semver ranges or exact).
	Version []string `json:"version,omitempty"`

	// Specs is a list of IRIs for package metadata.
	Specs []string `json:"specs,omitempty"`
}

// NetworkAccessRequirement indicates network access needs.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#NetworkAccessRequirement
type NetworkAccessRequirement struct {
	Class string `json:"class,omitempty"` // "NetworkAccessRequirement"

	// NetworkAccess indicates if network access is required.
	NetworkAccess any `json:"networkAccess"` // bool or expression
}

// WorkReuseRequirement controls output caching.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#WorkReuseRequirement
type WorkReuseRequirement struct {
	Class string `json:"class,omitempty"` // "WorkReuseRequirement"

	// EnableReuse controls whether outputs can be reused.
	EnableReuse any `json:"enableReuse"` // bool or expression
}

// ToolTimeLimitRequirement specifies execution time limits.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#ToolTimeLimitRequirement
type ToolTimeLimitRequirement struct {
	Class string `json:"class,omitempty"` // "ToolTimeLimitRequirement"

	// Timelimit is the maximum execution time in seconds.
	Timelimit any `json:"timelimit"` // int or expression
}

// SubworkflowFeatureRequirement enables nested workflows.
// See https://www.commonwl.org/v1.2/Workflow.html#SubworkflowFeatureRequirement
type SubworkflowFeatureRequirement struct {
	Class string `json:"class,omitempty"` // "SubworkflowFeatureRequirement"
}

// ScatterFeatureRequirement enables scatter operations in workflows.
// See https://www.commonwl.org/v1.2/Workflow.html#ScatterFeatureRequirement
type ScatterFeatureRequirement struct {
	Class string `json:"class,omitempty"` // "ScatterFeatureRequirement"
}

// MultipleInputFeatureRequirement enables merging multiple sources into a single parameter.
// See https://www.commonwl.org/v1.2/Workflow.html#MultipleInputFeatureRequirement
type MultipleInputFeatureRequirement struct {
	Class string `json:"class,omitempty"` // "MultipleInputFeatureRequirement"
}

// StepInputExpressionRequirement enables valueFrom on step inputs.
// See https://www.commonwl.org/v1.2/Workflow.html#StepInputExpressionRequirement
type StepInputExpressionRequirement struct {
	Class string `json:"class,omitempty"` // "StepInputExpressionRequirement"
}

// InplaceUpdateRequirement allows tools to modify input files in-place.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#InplaceUpdateRequirement
type InplaceUpdateRequirement struct {
	Class string `json:"class,omitempty"` // "InplaceUpdateRequirement"

	// InplaceUpdate controls whether in-place update is enabled.
	InplaceUpdate bool `json:"inplaceUpdate"`
}

// LoadListingRequirement specifies directory listing behavior.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#LoadListingRequirement
type LoadListingRequirement struct {
	Class string `json:"class,omitempty"` // "LoadListingRequirement"

	// LoadListing controls how directory listings are loaded.
	// Values: "no_listing", "shallow_listing", "deep_listing"
	LoadListing string `json:"loadListing,omitempty"`
}
