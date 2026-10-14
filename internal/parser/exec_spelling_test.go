package parser

import "testing"

// Every accepted spelling of gowe:Execution is read by extractStepHints, not
// only by the token-request detection.
func TestExtractStepHints_AllExecutionSpellings(t *testing.T) {
	for _, key := range []string{"gowe:Execution", "goweHint", "https://gowe.commonwl.org#Execution"} {
		h, err := extractStepHints(map[string]any{key: map[string]any{"worker_group": "g", "inject_bvbrc_token": true}}, nil)
		if err != nil || h == nil || h.WorkerGroup != "g" || !h.InjectBVBRCToken {
			t.Errorf("%s: %+v %v", key, h, err)
		}
		if !IsExecutionClass(key) {
			t.Errorf("%s not recognised", key)
		}
	}
}
