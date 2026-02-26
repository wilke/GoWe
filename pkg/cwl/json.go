// Package cwl provides CWL-specific JSON formatting utilities.
package cwl

import (
	"encoding/json"
	"math"
	"strconv"
)

// ConvertForCWLOutput recursively converts values for CWL-compliant JSON output:
// - float64 values are converted to json.Number to avoid scientific notation
// - NaN and Inf values are converted to null (not valid in JSON)
//
// This ensures large numbers like 4200000000000000000000000000000000000000000
// are output as full decimals rather than scientific notation (4.2e+42).
func ConvertForCWLOutput(v any) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range val {
			result[k] = ConvertForCWLOutput(v)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, v := range val {
			result[i] = ConvertForCWLOutput(v)
		}
		return result
	case float64:
		// NaN and Inf are not valid JSON - convert to null.
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return nil
		}
		// Format without scientific notation.
		// 'f' format produces decimal notation, -1 precision uses smallest
		// number of digits necessary to represent the value exactly.
		return json.Number(strconv.FormatFloat(val, 'f', -1, 64))
	default:
		return v
	}
}

// MarshalCWLOutput marshals output data to CWL-compliant JSON.
// It converts float64 values to avoid scientific notation.
func MarshalCWLOutput(v any) ([]byte, error) {
	converted := ConvertForCWLOutput(v)
	return json.MarshalIndent(converted, "", "  ")
}
