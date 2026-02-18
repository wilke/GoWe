package cwlexpr

// Context holds the evaluation context for CWL expressions.
// This includes the inputs object, self reference, and runtime information.
type Context struct {
	// Inputs is the inputs object containing all input parameter values.
	// Keys are input parameter IDs, values are the resolved values.
	Inputs map[string]any

	// Self is the current value being processed (used in valueFrom, outputEval).
	// For inputBinding.valueFrom, self is the input parameter value.
	// For outputBinding.outputEval, self is the collected output files.
	Self any

	// Runtime contains runtime information about the execution environment.
	Runtime *RuntimeContext

	// InOutputEval indicates if we're in an outputEval context.
	// When true, runtime.exitCode is included in the runtime object.
	InOutputEval bool
}

// RuntimeContext provides runtime information available to CWL expressions.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#Runtime_environment
type RuntimeContext struct {
	// OutDir is the designated output directory.
	OutDir string `json:"outdir"`

	// TmpDir is the designated temporary directory.
	TmpDir string `json:"tmpdir"`

	// Cores is the number of CPU cores allocated.
	Cores int `json:"cores"`

	// Ram is the amount of RAM in mebibytes allocated.
	Ram int64 `json:"ram"`

	// OutdirSize is the amount of storage in mebibytes in output directory.
	OutdirSize int64 `json:"outdirSize"`

	// TmpdirSize is the amount of storage in mebibytes in temp directory.
	TmpdirSize int64 `json:"tmpdirSize"`

	// ExitCode is the exit code from the command execution.
	// Only available in outputEval context.
	ExitCode int `json:"exitCode"`
}

// NewContext creates a new evaluation context with the given inputs.
func NewContext(inputs map[string]any) *Context {
	return &Context{
		Inputs:  inputs,
		Runtime: DefaultRuntimeContext(),
	}
}

// WithSelf returns a new context with the self value set.
func (c *Context) WithSelf(self any) *Context {
	return &Context{
		Inputs:       c.Inputs,
		Self:         self,
		Runtime:      c.Runtime,
		InOutputEval: c.InOutputEval,
	}
}

// WithRuntime returns a new context with the runtime context set.
func (c *Context) WithRuntime(rt *RuntimeContext) *Context {
	return &Context{
		Inputs:       c.Inputs,
		Self:         c.Self,
		Runtime:      rt,
		InOutputEval: c.InOutputEval,
	}
}

// WithOutputEval returns a new context marked as being in outputEval.
// This enables access to runtime.exitCode.
func (c *Context) WithOutputEval() *Context {
	return &Context{
		Inputs:       c.Inputs,
		Self:         c.Self,
		Runtime:      c.Runtime,
		InOutputEval: true,
	}
}

// DefaultRuntimeContext returns a RuntimeContext with sensible defaults.
func DefaultRuntimeContext() *RuntimeContext {
	return &RuntimeContext{
		OutDir:     "/tmp/cwl-output",
		TmpDir:     "/tmp/cwl-tmp",
		Cores:      1,
		Ram:        1024,
		OutdirSize: 1024,
		TmpdirSize: 1024,
	}
}

// FileObject represents a CWL File object in expressions.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#File
type FileObject struct {
	Class          string        `json:"class"` // "File"
	Location       string        `json:"location,omitempty"`
	Path           string        `json:"path,omitempty"`
	Basename       string        `json:"basename,omitempty"`
	Dirname        string        `json:"dirname,omitempty"`
	Nameroot       string        `json:"nameroot,omitempty"`
	Nameext        string        `json:"nameext,omitempty"`
	Checksum       string        `json:"checksum,omitempty"`
	Size           int64         `json:"size,omitempty"`
	Contents       string        `json:"contents,omitempty"`
	Format         string        `json:"format,omitempty"`
	SecondaryFiles []any         `json:"secondaryFiles,omitempty"`
}

// DirectoryObject represents a CWL Directory object in expressions.
// See https://www.commonwl.org/v1.2/CommandLineTool.html#Directory
type DirectoryObject struct {
	Class    string `json:"class"` // "Directory"
	Location string `json:"location,omitempty"`
	Path     string `json:"path,omitempty"`
	Basename string `json:"basename,omitempty"`
	Listing  []any  `json:"listing,omitempty"`
}

// NewFileObject creates a FileObject from a file path.
func NewFileObject(path string) *FileObject {
	basename := extractBasename(path)
	dirname := extractDirname(path)
	nameroot, nameext := splitExtension(basename)

	return &FileObject{
		Class:    "File",
		Path:     path,
		Location: "file://" + path,
		Basename: basename,
		Dirname:  dirname,
		Nameroot: nameroot,
		Nameext:  nameext,
	}
}

// NewDirectoryObject creates a DirectoryObject from a path.
func NewDirectoryObject(path string) *DirectoryObject {
	return &DirectoryObject{
		Class:    "Directory",
		Path:     path,
		Location: "file://" + path,
		Basename: extractBasename(path),
	}
}

// extractBasename returns the last component of a path.
func extractBasename(path string) string {
	if path == "" {
		return ""
	}
	// Remove trailing slash
	path = trimTrailingSlash(path)
	// Find last slash
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// extractDirname returns the directory portion of a path.
func extractDirname(path string) string {
	if path == "" {
		return ""
	}
	// Find last slash
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			if i == 0 {
				return "/"
			}
			return path[:i]
		}
	}
	return "."
}

// splitExtension splits basename into nameroot and nameext.
func splitExtension(basename string) (nameroot, nameext string) {
	if basename == "" {
		return "", ""
	}
	// Find last dot (but not first character)
	for i := len(basename) - 1; i > 0; i-- {
		if basename[i] == '.' {
			return basename[:i], basename[i:]
		}
	}
	return basename, ""
}

// trimTrailingSlash removes trailing slashes from a path.
func trimTrailingSlash(path string) string {
	for len(path) > 1 && path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	return path
}
