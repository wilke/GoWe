package cwl

import (
	"encoding/json"
	"fmt"
)

// InputBinding controls how an input parameter is converted to command-line argument(s).
// See https://www.commonwl.org/v1.2/CommandLineTool.html#CommandLineBinding
type InputBinding struct {
	// Position determines the relative ordering of arguments on the command line.
	// Arguments with lower position values appear before those with higher values.
	// Position 0 follows baseCommand and arguments with no position.
	// Can be an integer or a CWL expression.
	Position any `json:"position,omitempty"`

	// Prefix is a string to prepend to the input value (e.g., "--input" or "-i").
	Prefix string `json:"prefix,omitempty"`

	// Separate controls whether there is a space between prefix and value.
	// Default is true; if false, prefix and value are concatenated (e.g., "-i=value").
	Separate *bool `json:"separate,omitempty"`

	// ItemSeparator specifies how array items are joined when Separate is false.
	// Only applies when the input is an array type.
	ItemSeparator string `json:"itemSeparator,omitempty"`

	// ValueFrom is a CWL expression to compute the argument value.
	// Can be a parameter reference $(inputs.x) or JavaScript expression ${...}.
	ValueFrom string `json:"valueFrom,omitempty"`

	// ShellQuote controls whether the value is shell-quoted.
	// Default is true. Set to false for shell operators or patterns that should not be quoted.
	// Only has effect when ShellCommandRequirement is in effect.
	ShellQuote *bool `json:"shellQuote,omitempty"`

	// LoadContents loads the file content into the inputs object if the input type is File.
	LoadContents bool `json:"loadContents,omitempty"`
}

// OutputBinding specifies how to find and collect output files after tool execution.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#CommandOutputBinding
type OutputBinding struct {
	// Glob is a pattern (or list of patterns) to match output files in the output directory.
	// Can be a string, array of strings, or a CWL expression.
	Glob any `json:"glob,omitempty"`

	// LoadContents reads the first 64 KiB of the file into the file object's contents field.
	LoadContents bool `json:"loadContents,omitempty"`

	// LoadListing controls directory listing behavior when the output is a Directory.
	// Values: "no_listing", "shallow_listing", "deep_listing".
	LoadListing string `json:"loadListing,omitempty"`

	// OutputEval is a CWL expression to transform the collected output.
	// The expression has access to `self` (the collected files) and `inputs`.
	OutputEval string `json:"outputEval,omitempty"`
}

// Argument represents a structured command-line argument (CommandLineBinding).
// See https://www.commonwl.org/v1.2/CommandLineTool.html#CommandLineBinding
type Argument struct {
	// Position determines the ordering of this argument relative to other arguments and inputs.
	// Can be an integer or a CWL expression.
	Position any `json:"position,omitempty"`

	// Prefix is prepended to ValueFrom result if present.
	Prefix string `json:"prefix,omitempty"`

	// Separate controls whether there is a space between prefix and value.
	Separate *bool `json:"separate,omitempty"`

	// ValueFrom is the expression or literal value for this argument.
	// Required for structured arguments.
	ValueFrom string `json:"valueFrom,omitempty"`

	// ShellQuote controls whether the value is shell-quoted.
	ShellQuote *bool `json:"shellQuote,omitempty"`
}

// ArgumentEntry represents a CWL argument entry, which can be:
// - A string literal (used directly as command-line argument)
// - A CWL expression (evaluated at runtime)
// - A CommandLineBinding object (structured argument with position, prefix, etc.)
//
// This type enforces the CWL v1.2 spec: array<string | Expression | CommandLineBinding>
// See https://www.commonwl.org/v1.2/CommandLineTool.html
type ArgumentEntry struct {
	// StringValue holds the value if this is a string literal or expression.
	// Non-empty when IsString is true.
	StringValue string

	// Binding holds the value if this is a structured CommandLineBinding.
	// Non-nil when IsString is false.
	Binding *Argument

	// IsString indicates whether this entry is a string/expression (true) or binding (false).
	IsString bool
}

// UnmarshalJSON implements custom JSON unmarshaling for ArgumentEntry.
// It handles the CWL polymorphic type: string | Expression | CommandLineBinding.
func (a *ArgumentEntry) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as string first.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		a.StringValue = s
		a.IsString = true
		return nil
	}

	// Try to unmarshal as CommandLineBinding object.
	var binding Argument
	if err := json.Unmarshal(data, &binding); err == nil {
		a.Binding = &binding
		a.IsString = false
		return nil
	}

	return fmt.Errorf("argument must be string, expression, or CommandLineBinding object")
}

// MarshalJSON implements custom JSON marshaling for ArgumentEntry.
func (a ArgumentEntry) MarshalJSON() ([]byte, error) {
	if a.IsString {
		return json.Marshal(a.StringValue)
	}
	return json.Marshal(a.Binding)
}

// SecondaryFileSchema specifies secondary files associated with a File input/output.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#SecondaryFileSchema
type SecondaryFileSchema struct {
	// Pattern is a string or expression that defines the secondary file path.
	// Caret (^) removes extensions; if pattern starts with ^, remove extension first.
	Pattern string `json:"pattern"`

	// Required indicates whether the secondary file must exist (default true).
	// Can be a boolean or expression returning boolean.
	Required any `json:"required,omitempty"`
}

// Dirent represents an entry in InitialWorkDirRequirement listing.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#Dirent
type Dirent struct {
	// Entryname is the name of the file or directory to create.
	// Can be an expression.
	Entryname string `json:"entryname,omitempty"`

	// Entry is the content of the file, a File/Directory literal, or an expression.
	Entry any `json:"entry"`

	// Writable makes the entry writable (copy instead of link).
	Writable bool `json:"writable,omitempty"`
}

// EnvironmentDef defines an environment variable for EnvVarRequirement.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#EnvironmentDef
type EnvironmentDef struct {
	// EnvName is the name of the environment variable.
	EnvName string `json:"envName"`

	// EnvValue is the value (can be an expression).
	EnvValue string `json:"envValue"`
}

// RecordField represents a field in a CWL record type.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#CommandInputRecordField
type RecordField struct {
	// Name is the field name.
	Name string `json:"name"`

	// Type is the field type (e.g., "string", "int", "File").
	Type string `json:"type"`

	// InputBinding controls how this field appears on the command line.
	InputBinding *InputBinding `json:"inputBinding,omitempty"`

	// SecondaryFiles specifies additional files associated with this field (for File types).
	SecondaryFiles []SecondaryFileSchema `json:"secondaryFiles,omitempty"`

	// Format specifies the file format (for File types).
	Format any `json:"format,omitempty"`

	// Doc is documentation for this field.
	Doc string `json:"doc,omitempty"`

	// Label is a human-readable label.
	Label string `json:"label,omitempty"`
}

// OutputRecordField represents a field in a CWL output record type.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#CommandOutputRecordField
type OutputRecordField struct {
	// Name is the field name.
	Name string `json:"name"`

	// Type is the field type (e.g., "File", "File[]", "Directory").
	Type string `json:"type"`

	// OutputBinding specifies how to collect this field's output.
	OutputBinding *OutputBinding `json:"outputBinding,omitempty"`

	// SecondaryFiles specifies additional files associated with this field.
	SecondaryFiles []SecondaryFileSchema `json:"secondaryFiles,omitempty"`

	// Format specifies the file format for File outputs.
	Format any `json:"format,omitempty"`

	// Doc is documentation for this field.
	Doc string `json:"doc,omitempty"`

	// Label is a human-readable label.
	Label string `json:"label,omitempty"`
}
