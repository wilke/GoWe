package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// TestOutputCollectionErrorMasksTokenBeforeCut: when output collection fails,
// the error carries the tail of the tool's stderr. A delegated token that
// straddles the tail's cut must not leave an unmasked fragment, so the token
// has to be masked before the cut (cfg.SecretValues), not only afterwards by
// the reported-log redaction.
func TestOutputCollectionErrorMasksTokenBeforeCut(t *testing.T) {
	var logs bytes.Buffer
	w := newCWLTestWorker(t, "", &logs)
	w.containerRuntime = "none"
	h := tokHints("", nil, nil)
	h.InjectBVBRCToken = false
	// The token, then just under 2048 bytes, so the 2 KB cut falls inside it.
	pad := 2048 - len(testToken)/2
	shell := fmt.Sprintf("printf '%%s' '%s' >&2; head -c %d /dev/zero | tr '\\0' y >&2", testToken, pad)
	tool := localEchoTool(shell)
	// typeSchema as the server sends it (tasks without one skip validation).
	tool["outputs"] = map[string]any{"must": map[string]any{"type": "string", "typeSchema": "string"}}
	task := &model.Task{ID: "task_tail", Tool: tool, Job: map[string]any{}, RuntimeHints: h}

	res := runCWLTask(t, w, task)
	if res.State == model.TaskStateSuccess {
		t.Fatalf("expected a failure for the missing required output")
	}
	reported, _ := json.Marshal(res)
	for i := 6; i <= len(testToken); i++ {
		if frag := testToken[len(testToken)-i:]; strings.Contains(string(reported), frag) {
			t.Fatalf("reported result leaks the end of the token (%q)", frag)
		}
	}
	if !strings.Contains(string(reported), "required output missing") {
		t.Fatalf("expected the output-validation error in the report: %s", reported)
	}
}
