// Package validate provides CWL input validation utilities.
// This package is used by both cwl-runner and the distributed execution engine.
package validate

import (
	"fmt"
	"strings"

	"github.com/me/gowe/pkg/cwl"
)

// ToolInputs validates that inputs match the tool's input schema.
// Returns an error if required inputs are missing or null is provided for non-optional types.
func ToolInputs(tool *cwl.CommandLineTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		value, exists := inputs[inputID]

		// Check if input is optional (type ends with ? or is a union with null).
		isOptional := IsOptionalType(inputDef.Type)

		// Check for missing required inputs.
		if !exists {
			if inputDef.Default == nil && !isOptional {
				return fmt.Errorf("missing required input: %s", inputID)
			}
			continue
		}

		// Check for null values on non-optional inputs.
		// Exception: type "Any" with a default value - null means "use the default".
		if value == nil && !isOptional {
			if inputDef.Type == "Any" && inputDef.Default != nil {
				continue // null is allowed for Any with default - it will use the default.
			}
			return fmt.Errorf("null is not valid for non-optional input: %s (type: %s)", inputID, inputDef.Type)
		}
	}
	return nil
}

// ExpressionToolInputs validates that inputs match the ExpressionTool's input schema.
// Returns an error if required inputs are missing or null is provided for non-optional types.
func ExpressionToolInputs(tool *cwl.ExpressionTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		value, exists := inputs[inputID]

		// Check if input is optional (type ends with ? or is a union with null).
		isOptional := IsOptionalType(inputDef.Type)

		// Check for missing required inputs.
		if !exists {
			if inputDef.Default == nil && !isOptional {
				return fmt.Errorf("missing required input: %s", inputID)
			}
			continue
		}

		// Check for null values on non-optional inputs.
		// Exception: type "Any" with a default value - null means "use the default".
		if value == nil && !isOptional {
			if inputDef.Type == "Any" && inputDef.Default != nil {
				continue // null is allowed for Any with default - it will use the default.
			}
			return fmt.Errorf("null is not valid for non-optional input: %s (type: %s)", inputID, inputDef.Type)
		}
	}
	return nil
}

// IsOptionalType checks if a CWL type is optional (can be null).
// Types ending with ? or types that are unions including null are optional.
func IsOptionalType(t string) bool {
	if t == "" {
		return false
	}
	// Type ending with ? is optional.
	if strings.HasSuffix(t, "?") {
		return true
	}
	// "null" type itself is optional.
	if t == "null" {
		return true
	}
	return false
}
