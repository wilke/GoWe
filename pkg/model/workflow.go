package model

import "time"

// Workflow is a parsed, validated CWL workflow definition stored in GoWe.
type Workflow struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	CWLVersion  string           `json:"cwl_version"`
	RawCWL      string           `json:"-"` // Original CWL document (not exposed in API list views)
	Inputs      []WorkflowInput  `json:"inputs"`
	Outputs     []WorkflowOutput `json:"outputs"`
	Steps       []Step           `json:"steps"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// WorkflowInput describes a typed input parameter of a Workflow.
type WorkflowInput struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  any    `json:"default,omitempty"`
}

// WorkflowOutput describes a typed output of a Workflow.
type WorkflowOutput struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	OutputSource string `json:"output_source"`
}

// Step is a single node in a Workflow's DAG.
type Step struct {
	ID        string     `json:"id"`
	ToolRef   string     `json:"tool_ref"`
	ToolInline *Tool     `json:"-"` // Resolved tool (not serialized in API responses)
	DependsOn []string   `json:"depends_on"`
	In        []StepInput  `json:"in"`
	Out       []string     `json:"out"`
	Scatter   []string     `json:"scatter,omitempty"`
	When      string       `json:"when,omitempty"`
	Hints     *StepHints   `json:"hints,omitempty"`
}

// StepInput maps a step input to its source.
type StepInput struct {
	ID     string `json:"id"`
	Source string `json:"source"`
}

// StepHints holds GoWe-specific hints extracted from a CWL step.
type StepHints struct {
	BVBRCAppID   string       `json:"bvbrc_app_id,omitempty"`
	ExecutorType ExecutorType `json:"executor,omitempty"`
}

// Tool represents a CWL CommandLineTool or ExpressionTool.
type Tool struct {
	ID          string        `json:"id"`
	Class       string        `json:"class"` // "CommandLineTool" or "ExpressionTool"
	BaseCommand []string      `json:"base_command,omitempty"`
	Inputs      []ToolInput   `json:"inputs"`
	Outputs     []ToolOutput  `json:"outputs"`
	Hints       *StepHints    `json:"hints,omitempty"`
}

// ToolInput describes an input parameter of a Tool.
type ToolInput struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Default any    `json:"default,omitempty"`
	Doc     string `json:"doc,omitempty"`
}

// ToolOutput describes an output of a Tool.
type ToolOutput struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Glob string `json:"glob,omitempty"`
}
