# GoWe Project Scratchpad

## Current Session: File Literals Implementation

### Final Status
- **cwl-runner**: 84/84 passing (100%) ✓
- **server-local**: 58/84 passing (69%) - up from 52

### Completed Fixes This Session

1. **File Literals Support** (NEW shared package)
   - Created `internal/fileliteral/literal.go` with shared implementation
   - File literals are CWL File objects with `contents` but no `path`/`location`
   - These must be materialized as temp files before execution
   - Functions: `Materialize()`, `MaterializeFileObject()`, `MaterializeRecursive()`

2. **Integration Points Updated**
   - `internal/cwlrunner/runner.go` - Uses shared fileliteral package
   - `internal/stepinput/resolve.go` - Materializes file literals during input resolution
   - `internal/iwdr/stage.go` - Materializes file literals during IWDR staging
   - `internal/execution/stager.go` - Materializes file literals during input staging
   - `internal/server/handler_submissions.go` - **Critical fix**: Materializes at submission time

### Fixed Tests (+6 tests)
- Test 22: `input_file_literal` - Basic file literal as input
- Test 29: `fileliteral_input_docker` - File literal without Docker requirement
- Test 45: `stdin_from_directory_literal_with_literal_file` - File literal in Directory listing via stdin
- Test 46: `directory_literal_with_literal_file_nostdin` - File literal in Directory via valueFrom
- Test 68: `directory_literal_with_literal_file_in_subdir_nostdin` - Nested directory with file literal
- Plus one additional test fix from previous work

### Shared Code Between cwl-runner and Server

The following packages are shared and changes affect both:

| Package | Purpose | File Literal Support |
|---------|---------|---------------------|
| `internal/fileliteral/` | **NEW** - File literal materialization | Core implementation |
| `internal/bundle/` | CWL bundling | - |
| `internal/parser/` | CWL parsing | - |
| `internal/stepinput/` | Step input resolution | Uses fileliteral |
| `internal/execution/` | CWL tool execution | Uses fileliteral |
| `internal/iwdr/` | InitialWorkDirRequirement | Uses fileliteral |
| `internal/cwloutput/` | Workflow output collection | - |
| `pkg/cwl/` | CWL type definitions | - |

### Remaining Failures (26 tests)

| Category | Count | Tests |
|----------|-------|-------|
| Platform difference (macOS sort) | 2 | 10, 27 |
| Format checking | 3 | 14, 15, 16 |
| Nested prefix arrays | 1 | 2 |
| Directory output listing | 3 | 21, 57, 79 |
| Undeclared params | 1 | 37 |
| Any without defaults | 2 | 38, 39 |
| Null step workflows | 2 | 40, 43 |
| Record secondaryFiles | 2 | 53, 55 |
| Position expression | 1 | 58 |
| Synth file | 1 | 62 |
| LoadContents limit | 1 | 64 |
| Colon in paths | 2 | 69, 70 |
| Paramref arguments | 2 | 71, 84 |
| Runtime outdir | 1 | 73 |
| Record order | 1 | 74 |
| Workflow output reference | 1 | 75 |
| Octo yml | 1 | 76 |

### Key Files Modified This Session

```
internal/fileliteral/literal.go           # NEW - shared file literal implementation
internal/cwlrunner/runner.go              # Uses shared fileliteral
internal/stepinput/resolve.go             # Uses shared fileliteral
internal/iwdr/stage.go                    # Uses shared fileliteral
internal/execution/stager.go              # Uses shared fileliteral
internal/server/handler_submissions.go    # Materializes at submission time
```

### Previous Session Work

Fragment URL handling, empty steps array, tool lookup fix, zero-tasks handling.
See git history for details.
