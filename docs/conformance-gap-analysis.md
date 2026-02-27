# CWL Conformance Gap Analysis

**Date**: 2026-02-27
**Baseline**: Commit 75f4e6f (also 0db6d51)
**Status**: 268/378 passed (71%), 110 failures

## Summary by Category

| Category | Failing | Component (New Architecture) |
|----------|---------|------------------------------|
| expression_tool_js | 22 | `cwlexpr/` - Expression engine |
| inline_javascript | 11 | `cwlexpr/` - Expression engine |
| shell_command | 10 | `runtime/` - Shell execution |
| resource | 8 | `requirements/` - Resource reqs |
| subworkflow | 7 | `runner/` - Workflow orchestration |
| timelimit | 6 | `runtime/` - Process control |
| other | 6 | Various |
| env_var | 4 | `requirements/` + `runtime/` |
| format_checking | 4 | `parser/` - Validation |
| input_object_requirements | 3 | `requirements/` |
| scatter | 2 | `runner/` - Scatter execution |
| multiple_input | 2 | `runner/` - Step inputs |
| step_input | 2 | `stepinput/` |
| load_listing | 2 | `staging/` - Directory listing |
| schema_def | 1 | `parser/` - Type handling |
| networkaccess | 1 | `runtime/` - Network control |
| inplace_update | 1 | `staging/` - File handling |
| **Version/syntax tests** | 18 | `parser/` - Version validation |

**Total: 110 failing tests**

## Mapping to New Architecture

### 1. Expression Engine (`internal/cwlexpr/`) - 33 tests

The largest gap. Issues include:
- ExpressionTool with Any type inputs
- parseInt and other JS functions
- Null handling in expressions
- Expression evaluation in various contexts

**Fix approach**: Enhance the JavaScript expression evaluator to handle edge cases.

### 2. Runtime (`internal/runtime/`) - 17 tests

- ShellCommandRequirement (proper shell escaping)
- TimeLimit enforcement
- NetworkAccess control
- $HOME/$TMPDIR environment setup

**Fix approach**: Create dedicated runtime package with proper process control.

### 3. Requirements (`internal/requirements/`) - 15 tests

- Dynamic resource requirements (expressions in cores/ram)
- EnvVarRequirement propagation
- InputObjectRequirements from job files

**Fix approach**: Extract and consolidate requirement handling.

### 4. Parser/Validator (`internal/parser/`) - 23 tests

- SchemaDefRequirement (nested types)
- Format checking on inputs/outputs
- Version syntax validation (v1.0 vs v1.2)
- Invalid syntax detection

**Fix approach**: Enhance validation during parsing.

### 5. Runner/Workflow (`internal/runner/`) - 11 tests

- Subworkflow execution
- Scatter with multiple inputs
- Nested cross-product scatter

**Fix approach**: Fix workflow orchestration logic.

### 6. Staging (`internal/staging/`) - 3 tests

- Directory listing (loadListing)
- Inplace updates
- Symlink handling

**Fix approach**: Consolidate staging logic.

### 7. Step Input (`internal/stepinput/`) - 2 tests

- Multiple sources with multiple types
- Already partially implemented

## Recommended Fix Order

### Phase 1: Low-hanging fruit (quick wins)
1. **env_var** (4 tests) - EnvVarRequirement propagation
2. **schema_def** (1 test) - Nested type handling in cmdline
3. **load_listing** (2 tests) - Directory listing support

### Phase 2: Expression engine overhaul
4. **expression_tool_js** (22 tests) - Fix Any type, null handling
5. **inline_javascript** (11 tests) - Expression contexts

### Phase 3: Runtime features
6. **shell_command** (10 tests) - Shell escaping/execution
7. **timelimit** (6 tests) - Process timeouts
8. **resource** (8 tests) - Dynamic resource expressions

### Phase 4: Workflow features
9. **subworkflow** (7 tests) - Nested workflow execution
10. **scatter/multiple_input** (4 tests) - Complex scatter

### Phase 5: Validation
11. **format_checking** (4 tests) - Input/output format validation
12. **Version tests** (18 tests) - Syntax version checking

## Integration with Refactoring

The recommended approach:

1. **Start with Phase 1 fixes** on current codebase (establishes patterns)
2. **Extract `requirements/` package** while fixing env_var/resource tests
3. **Extract `runtime/` package** while fixing shell_command/timelimit
4. **Extract `cwlexpr/` enhancements** while fixing expression tests
5. **Consolidate `runner/`** while fixing subworkflow/scatter
6. **Extract `staging/`** while fixing load_listing/inplace

This way each extraction comes with its tests fixed, and we never have a regression.
