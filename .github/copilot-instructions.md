# GitHub Copilot Custom Instructions for GoWe

## Project Overview

GoWe is a Common Workflow Language (CWL) v1.2 workflow engine for BV-BRC bioinformatics pipelines, written in Go. The project parses and executes CWL workflow definitions, schedules tasks across multiple backends (local, Docker, BV-BRC), and provides a REST API for workflow management.

### Key Technologies
- **Language:** Go 1.24+
- **Web Framework:** go-chi/chi for REST API routing
- **Database:** SQLite with modernc.org/sqlite (pure Go, no CGO)
- **CLI:** cobra for command-line interface
- **Testing:** Go standard testing package
- **Containerization:** Docker (optional, for container executor)

## Build & Test Commands

### Building the Project
```bash
# Build server
go build -o bin/gowe-server ./cmd/server

# Build CLI client
go build -o bin/gowe ./cmd/cli

# Build both
mkdir -p bin
go build -o bin/gowe-server ./cmd/server
go build -o bin/gowe ./cmd/cli
```

### Running Tests
```bash
# Run all unit tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run tests for a specific package
go test ./internal/parser/...

# Run integration tests (requires Docker)
go test ./internal/executor/ -tags=integration

# Run BV-BRC integration tests (requires valid token)
BVBRC_TOKEN=... go test ./internal/executor/ -tags=integration
```

### Linting and Formatting
```bash
# Format code
go fmt ./...

# Run go vet to check for common issues
go vet ./...
```

### Running the Application
```bash
# Start the server (default port 8080)
./bin/gowe-server

# Start with custom configuration
./bin/gowe-server --addr :9090 --debug --db /tmp/gowe.db

# Use CLI client
./bin/gowe --help
./bin/gowe status <submission_id>
```

## Coding Standards

### Go Conventions
- Follow standard Go conventions and idioms
- Use `gofmt` for code formatting (never manually format)
- Use meaningful variable and function names
- Prefer short variable names in small scopes (e.g., `i`, `err`, `ctx`)
- Use longer descriptive names for package-level variables and exported functions
- Always handle errors explicitly; never ignore errors
- Use `context.Context` for cancellation and timeouts in long-running operations

### Error Handling
- Return errors as the last return value
- Wrap errors with context using `fmt.Errorf("context: %w", err)`
- Use custom error types when needed for error handling logic
- Log errors with appropriate context before returning them

### Logging
- Use the `internal/logging` package for structured logging
- Use appropriate log levels: debug, info, warn, error
- Include relevant context in log messages (e.g., workflow ID, task ID)
- Never log sensitive information (tokens, credentials, passwords)

### Testing
- Write table-driven tests for multiple test cases
- Use `t.Run()` for subtests
- Mock external dependencies (HTTP clients, executors, etc.)
- Keep unit tests fast and independent
- Use integration tests (with build tags) for Docker and BV-BRC executors
- Test files should be named `*_test.go`
- Test functions should start with `Test`
- Benchmark functions should start with `Benchmark`

### Naming Conventions
- Package names: lowercase, single word, no underscores
- Exported functions/types: PascalCase
- Unexported functions/types: camelCase
- Constants: PascalCase (not SCREAMING_SNAKE_CASE)
- File names: lowercase with underscores (e.g., `workflow_handler.go`)
- Test files: `*_test.go`
- Integration tests: `*_integration_test.go` with build tags

### Code Organization
- Keep functions small and focused
- Group related functionality in the same file
- Use internal packages for code not meant to be imported by external projects
- Prefer composition over inheritance
- Use interfaces for abstraction and testing

## Project Structure

```
cmd/
  server/       Server entrypoint (main.go)
  cli/          CLI entrypoint (main.go)
  scheduler/    Standalone scheduler (future use)
internal/
  bvbrc/        BV-BRC authentication and JSON-RPC 1.1 client
  bundle/       CWL file bundler for packing workflows
  config/       Server configuration management
  executor/     Executor backends (local, docker, bvbrc)
  logging/      Structured logging setup with slog
  parser/       CWL parser, validator, and DAG builder
  scheduler/    Tick-based task scheduler with dependency resolution
  server/       HTTP handlers, routing, and middleware
  store/        SQLite persistence layer
  cli/          CLI command implementations
pkg/
  cwl/          CWL v1.2 type definitions
  model/        Domain models (Workflow, Task, Submission, state machines)
testdata/       Example CWL workflows for testing
docs/           Documentation and implementation plans
```

### Directory Guidelines
- **cmd/** — Application entry points only, minimal logic
- **internal/** — Private application code, not importable by external projects
- **pkg/** — Public libraries that could be imported by external projects
- **testdata/** — Test fixtures and example data; never modify production CWL files here without good reason
- **docs/** — Documentation files; update when making significant changes to architecture

## Key Architectural Concepts

### Workflow Execution Flow
1. Workflow registration (via API or CLI)
2. Submission creation with inputs
3. Task scheduling based on dependencies
4. Executor backend selection (local/docker/bvbrc)
5. Task execution with state machine transitions
6. Output collection and result reporting

### State Machines
- **Submission states:** pending → running → completed/failed/cancelled
- **Task states:** pending → ready → running → completed/failed
- State transitions are handled atomically in the database

### Executors
- **local:** Runs commands as OS processes
- **container:** Runs commands in Docker containers (requires DockerRequirement)
- **bvbrc:** Submits jobs to BV-BRC platform (requires authentication)

## Dependencies

### Adding Dependencies
```bash
# Add a new dependency
go get github.com/example/package

# Update dependencies
go get -u ./...
go mod tidy
```

### Security Considerations
- Always run dependency vulnerability checks before adding new dependencies
- Prefer well-maintained, popular libraries
- Avoid dependencies with known security issues
- Use `go mod tidy` to remove unused dependencies

## Pull Request Guidelines

### Before Submitting
- Run `go test ./...` to ensure all tests pass
- Run `go fmt ./...` to format code
- Run `go vet ./...` to check for common issues
- Ensure the server builds successfully
- Test manually if adding new features or fixing bugs
- Check that no sensitive data is committed

### PR Requirements
- Reference the related issue number
- Include a clear description of changes
- Add or update tests for new functionality
- Update documentation if changing APIs or behavior
- Keep PRs focused and small (prefer multiple small PRs over one large PR)
- Ensure CI checks pass before requesting review

### Commit Messages
- Use clear, descriptive commit messages
- Start with a verb in present tense (e.g., "Add", "Fix", "Update", "Refactor")
- Keep the first line under 72 characters
- Add more details in the body if needed

## Task Scope for Copilot Agent

### Suitable Tasks
- Bug fixes in existing code
- Adding unit tests
- Refactoring for code clarity
- Documentation updates
- Adding new API endpoints following existing patterns
- Implementing new CWL features
- Performance improvements

### Requires Human Review
- Major architectural changes
- Changes to the scheduler algorithm
- Modifications to state machine logic
- Changes to the database schema (migrations)
- Security-sensitive code
- Breaking API changes

### Files to Never Modify
- `testdata/` files unless specifically working on test fixtures
- `.git/` directory
- `go.sum` (only `go.mod` should be edited manually, `go.sum` is auto-generated)

## Security Guidelines

### Sensitive Data
- **Never commit:** API keys, tokens, credentials, passwords, or other secrets
- **Never log:** Sensitive information like tokens or credentials
- **Never expose:** Internal error details in API responses to external users

### Authentication
- BV-BRC authentication tokens are stored in:
  - `BVBRC_TOKEN` environment variable
  - `~/.gowe/credentials.json`
  - `~/.bvbrc_token`, `~/.patric_token`, or `~/.p3_token`
- Always validate and sanitize user inputs
- Use context timeouts for external API calls

### Best Practices
- Follow OWASP security guidelines
- Validate all inputs from API requests
- Use prepared statements for database queries (already handled by the store package)
- Avoid command injection in executor backends
- Sanitize file paths to prevent directory traversal

## API Design

### Endpoints
- All API endpoints are prefixed with `/api/v1`
- Use standard HTTP methods: GET, POST, PUT, DELETE
- Return standard JSON envelope format:
  ```json
  {
    "status": "success",
    "request_id": "...",
    "timestamp": "...",
    "data": { ... }
  }
  ```

### Error Responses
- Use appropriate HTTP status codes
- Return error details in the standard envelope
- Include `request_id` for debugging
- Don't expose internal error details to external users

## CWL Specifications

### Supported CWL Version
- CWL v1.2 specification (https://www.commonwl.org/v1.2/)
- Packed or modular workflow formats
- Custom hints via `goweHint` for GoWe-specific behavior

### Executor Selection
Executors are selected based on CWL hints:
- `DockerRequirement` → container executor
- `goweHint.executor: bvbrc` → bvbrc executor
- Default → local executor

## Common Patterns

### Context Usage
```go
// Always pass context as the first parameter
func ProcessWorkflow(ctx context.Context, workflowID string) error {
    // Use context for cancellation and timeouts
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
        // Process workflow
    }
}
```

### Error Handling
```go
// Wrap errors with context
if err != nil {
    return fmt.Errorf("failed to process workflow %s: %w", workflowID, err)
}

// Log before returning errors
if err := doSomething(); err != nil {
    log.Error("operation failed", "error", err, "context", "value")
    return err
}
```

### Testing Patterns
```go
// Use table-driven tests
func TestFunction(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {name: "valid input", input: "test", want: "result", wantErr: false},
        {name: "invalid input", input: "", want: "", wantErr: true},
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Function(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("Function() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if got != tt.want {
                t.Errorf("Function() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

## Additional Notes

- This is an active project under development
- Breaking changes should be well-documented
- Maintain backward compatibility when possible
- Consider the impact on existing workflows and users
- Document any new CWL features or custom hints
- Keep the README.md up to date with significant changes
