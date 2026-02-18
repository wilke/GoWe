package execution

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

// LocalRuntime executes commands as local processes.
type LocalRuntime struct{}

// Run executes a command locally.
func (r *LocalRuntime) Run(ctx context.Context, spec RunSpec) (*RunResult, error) {
	if len(spec.Command) == 0 {
		return nil, ErrEmptyCommand
	}

	// Create working directory.
	if err := os.MkdirAll(spec.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("create workdir: %w", err)
	}

	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Dir = spec.WorkDir

	// Set environment.
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Handle stdin.
	if spec.Stdin != "" {
		stdinPath := spec.Stdin
		if !filepath.IsAbs(stdinPath) {
			stdinPath = filepath.Join(spec.WorkDir, stdinPath)
		}
		stdin, err := os.Open(stdinPath)
		if err != nil {
			return nil, fmt.Errorf("open stdin: %w", err)
		}
		defer stdin.Close()
		cmd.Stdin = stdin
	}

	// Handle stdout.
	var stdoutBuf bytes.Buffer
	if spec.Stdout != "" {
		stdoutPath := filepath.Join(spec.WorkDir, spec.Stdout)
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		cmd.Stdout = &stdoutBuf
	}

	// Handle stderr.
	var stderrBuf bytes.Buffer
	if spec.Stderr != "" {
		stderrPath := filepath.Join(spec.WorkDir, spec.Stderr)
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = &stderrBuf
	}

	// Run the command.
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("run command: %w", err)
		}
	}

	return &RunResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}, nil
}

// executeLocal executes a tool locally without Docker.
func (e *Engine) executeLocal(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, workDir string) (*RunResult, error) {
	e.logger.Info("executing locally", "command", cmdResult.Command)

	// Create working directory.
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, fmt.Errorf("create work directory: %w", err)
	}

	// Build the command.
	if len(cmdResult.Command) == 0 {
		return nil, ErrEmptyCommand
	}

	// Check for ShellCommandRequirement - run through shell if present.
	var cmd *exec.Cmd
	if hasShellCommandRequirement(tool) {
		cmdStr := strings.Join(cmdResult.Command, " ")
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, cmdResult.Command[0], cmdResult.Command[1:]...)
	}
	cmd.Dir = workDir

	// Set environment variables.
	cmd.Env = os.Environ()
	envVars := extractEnvVars(tool, inputs)
	for name, value := range envVars {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", name, value))
	}

	// Handle stdin.
	if cmdResult.Stdin != "" {
		stdinPath := cmdResult.Stdin
		if !filepath.IsAbs(stdinPath) {
			stdinPath = filepath.Join(workDir, stdinPath)
		}
		stdin, err := os.Open(stdinPath)
		if err != nil {
			return nil, fmt.Errorf("open stdin %s: %w", stdinPath, err)
		}
		defer stdin.Close()
		cmd.Stdin = stdin
	}

	// Determine stdout capture filename.
	stdoutCapture := cmdResult.Stdout
	if stdoutCapture == "" && hasStdoutOutput(tool) {
		stdoutCapture = "cwl.stdout.txt"
	}

	// Handle stdout.
	var stdoutBuf bytes.Buffer
	if stdoutCapture != "" {
		stdoutPath := filepath.Join(workDir, stdoutCapture)
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		cmd.Stdout = &stdoutBuf
	}

	// Determine stderr capture filename.
	stderrCapture := cmdResult.Stderr
	if stderrCapture == "" && hasStderrOutput(tool) {
		stderrCapture = "cwl.stderr.txt"
	}

	// Handle stderr.
	var stderrBuf bytes.Buffer
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = &stderrBuf
	}

	// Run command.
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("run command: %w", err)
		}
	}

	return &RunResult{
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}, nil
}

// extractEnvVars extracts environment variables from EnvVarRequirement.
func extractEnvVars(tool *cwl.CommandLineTool, inputs map[string]any) map[string]string {
	envVars := make(map[string]string)

	// Check requirements first (takes precedence).
	if envReq := getEnvVarRequirement(tool.Requirements); envReq != nil {
		processEnvDef(envReq, envVars, inputs)
	}

	// Check hints.
	if envReq := getEnvVarRequirement(tool.Hints); envReq != nil {
		for k, v := range processEnvDef(envReq, make(map[string]string), inputs) {
			if _, exists := envVars[k]; !exists {
				envVars[k] = v
			}
		}
	}

	return envVars
}

// getEnvVarRequirement extracts EnvVarRequirement from hints or requirements map.
func getEnvVarRequirement(reqMap map[string]any) map[string]any {
	if reqMap == nil {
		return nil
	}
	if req, ok := reqMap["EnvVarRequirement"].(map[string]any); ok {
		return req
	}
	return nil
}

// processEnvDef processes envDef from EnvVarRequirement.
func processEnvDef(envReq map[string]any, envVars map[string]string, inputs map[string]any) map[string]string {
	envDef, ok := envReq["envDef"]
	if !ok {
		return envVars
	}

	switch defs := envDef.(type) {
	case []any:
		for _, def := range defs {
			if m, ok := def.(map[string]any); ok {
				name, _ := m["envName"].(string)
				value, _ := m["envValue"].(string)
				if name != "" {
					envVars[name] = value
				}
			}
		}
	case map[string]any:
		for name, val := range defs {
			if value, ok := val.(string); ok {
				envVars[name] = value
			}
		}
	}

	return envVars
}

// stageFileOrDir stages a File or Directory in the working directory via symlink.
func stageFileOrDir(obj map[string]any, workDir string) error {
	path := ""
	if p, ok := obj["path"].(string); ok {
		path = p
	} else if loc, ok := obj["location"].(string); ok {
		path = loc
	}

	if path == "" {
		return nil
	}

	// If path is absolute, create symlink in workDir.
	if filepath.IsAbs(path) {
		return symlinkToWorkDir(path, workDir)
	}

	// If path is already in workDir, nothing to do.
	absPath, _ := filepath.Abs(path)
	absWorkDir, _ := filepath.Abs(workDir)
	if strings.HasPrefix(absPath, absWorkDir) {
		return nil
	}

	// Create symlink in workDir for relative paths.
	basename := filepath.Base(path)
	linkPath := filepath.Join(workDir, basename)

	if _, err := os.Lstat(linkPath); err == nil {
		return nil // Link already exists
	}

	return os.Symlink(absPath, linkPath)
}

// loadFileContents loads the first 64KB of a file.
func loadFileContents(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, 64*1024)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	return string(buf[:n]), nil
}
