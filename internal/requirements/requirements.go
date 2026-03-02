// Package requirements provides helpers for extracting and checking CWL requirements and hints.
package requirements

import (
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// HasShellCommand checks if a tool has ShellCommandRequirement in requirements or hints.
func HasShellCommand(tool *cwl.CommandLineTool) bool {
	return HasRequirement(tool, "ShellCommandRequirement")
}

// HasNetworkAccess checks if a tool has NetworkAccessRequirement with networkAccess: true.
// CWL spec: Network access is disabled by default in containers.
func HasNetworkAccess(tool *cwl.CommandLineTool) bool {
	// Check requirements first.
	if tool.Requirements != nil {
		// Check both "NetworkAccessRequirement" and "NetworkAccess" keys.
		for _, key := range []string{"NetworkAccessRequirement", "NetworkAccess"} {
			if req, ok := tool.Requirements[key]; ok {
				if reqMap, ok := req.(map[string]any); ok {
					if na, ok := reqMap["networkAccess"]; ok {
						if b, ok := na.(bool); ok {
							return b
						}
					}
				}
			}
		}
	}
	// Check hints.
	if tool.Hints != nil {
		for _, key := range []string{"NetworkAccessRequirement", "NetworkAccess"} {
			if req, ok := tool.Hints[key]; ok {
				if reqMap, ok := req.(map[string]any); ok {
					if na, ok := reqMap["networkAccess"]; ok {
						if b, ok := na.(bool); ok {
							return b
						}
					}
				}
			}
		}
	}
	return false
}

// HasRequirement checks if a tool has the specified requirement in requirements or hints.
func HasRequirement(tool *cwl.CommandLineTool, name string) bool {
	if tool.Requirements != nil {
		if _, ok := tool.Requirements[name]; ok {
			return true
		}
	}
	if tool.Hints != nil {
		if _, ok := tool.Hints[name]; ok {
			return true
		}
	}
	return false
}

// GetRequirement returns the requirement with the given name from requirements or hints.
// Requirements take precedence over hints. Returns nil if not found.
func GetRequirement(tool *cwl.CommandLineTool, name string) map[string]any {
	if tool.Requirements != nil {
		if req, ok := tool.Requirements[name].(map[string]any); ok {
			return req
		}
	}
	if tool.Hints != nil {
		if req, ok := tool.Hints[name].(map[string]any); ok {
			return req
		}
	}
	return nil
}

// ExtractEnvVars extracts environment variables from EnvVarRequirement.
// It processes hints, then requirements, then job requirements (highest precedence).
// CWL expressions in envValue are evaluated using the provided inputs.
func ExtractEnvVars(tool *cwl.CommandLineTool, inputs map[string]any, jobRequirements []any) map[string]string {
	envVars := make(map[string]string)

	// Get expression library from InlineJavascriptRequirement if present.
	expressionLib := GetExpressionLib(tool)

	evaluator := cwlexpr.NewEvaluator(expressionLib)
	ctx := cwlexpr.NewContext(inputs)

	// Check hints first (lowest precedence)
	if envReq := getEnvVarRequirement(tool.Hints); envReq != nil {
		processEnvDef(envReq, envVars, evaluator, ctx)
	}

	// Check requirements (overrides hints)
	if envReq := getEnvVarRequirement(tool.Requirements); envReq != nil {
		processEnvDef(envReq, envVars, evaluator, ctx)
	}

	// Check job requirements (highest precedence, overrides tool requirements)
	for _, req := range jobRequirements {
		reqMap, ok := req.(map[string]any)
		if !ok {
			continue
		}
		class, _ := reqMap["class"].(string)
		if class == "EnvVarRequirement" {
			processEnvDef(reqMap, envVars, evaluator, ctx)
		}
	}

	return envVars
}

// GetExpressionLib extracts the expressionLib from InlineJavascriptRequirement.
func GetExpressionLib(tool *cwl.CommandLineTool) []string {
	var expressionLib []string
	if tool.Requirements != nil {
		if jsReq, ok := tool.Requirements["InlineJavascriptRequirement"].(map[string]any); ok {
			if lib, ok := jsReq["expressionLib"].([]any); ok {
				for _, l := range lib {
					if s, ok := l.(string); ok {
						expressionLib = append(expressionLib, s)
					}
				}
			}
		}
	}
	return expressionLib
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

// processEnvDef processes envDef from EnvVarRequirement and adds to envVars map.
func processEnvDef(envReq map[string]any, envVars map[string]string, evaluator *cwlexpr.Evaluator, ctx *cwlexpr.Context) {
	envDef, ok := envReq["envDef"]
	if !ok {
		return
	}

	// envDef can be an array or map
	switch defs := envDef.(type) {
	case []any:
		for _, def := range defs {
			if m, ok := def.(map[string]any); ok {
				name, _ := m["envName"].(string)
				value, _ := m["envValue"].(string)
				if name != "" {
					evaluated := evaluateEnvValue(value, evaluator, ctx)
					envVars[name] = evaluated
				}
			}
		}
	case map[string]any:
		for name, val := range defs {
			if value, ok := val.(string); ok {
				evaluated := evaluateEnvValue(value, evaluator, ctx)
				envVars[name] = evaluated
			}
		}
	}
}

// evaluateEnvValue evaluates a CWL expression in an environment variable value.
func evaluateEnvValue(value string, evaluator *cwlexpr.Evaluator, ctx *cwlexpr.Context) string {
	if !cwlexpr.IsExpression(value) {
		return value
	}

	result, err := evaluator.EvaluateString(value, ctx)
	if err != nil {
		// Return original value on error to avoid breaking execution.
		return value
	}
	return result
}
