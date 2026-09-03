package worker

import (
	"testing"

	"github.com/me/gowe/pkg/model"
)

// TestInjectBVBRCTokenEnv verifies the sole gate on BV-BRC token injection
// into a worker-executed tool's environment: the tool must explicitly opt in
// via gowe:Execution.inject_bvbrc_token (RuntimeHints.InjectBVBRCToken), and a
// token must actually be present. A token embedded in RuntimeHints for other
// reasons (e.g. legacy wsStager==nil staging, or a BV-BRC executor task) must
// NOT leak into a worker tool's environment absent that opt-in (#133 —
// restores the gate PR #132 dropped).
func TestInjectBVBRCTokenEnv(t *testing.T) {
	cred := &model.HTTPCredential{Type: "bearer", Token: "secret-token-value"}

	tests := []struct {
		name  string
		base  map[string]string
		hints *model.RuntimeHints
		want  map[string]string
	}{
		{
			name:  "hint set and token present: both vars injected",
			base:  nil,
			hints: &model.RuntimeHints{InjectBVBRCToken: true, StagerOverrides: &model.StagerOverrides{HTTPCredential: cred}},
			want:  map[string]string{"BVBRC_TOKEN": "secret-token-value", "KB_AUTH_TOKEN": "secret-token-value"},
		},
		{
			name:  "no hint, token present: neither var injected (#133)",
			base:  nil,
			hints: &model.RuntimeHints{StagerOverrides: &model.StagerOverrides{HTTPCredential: cred}},
			want:  nil,
		},
		{
			name:  "hint set but no token: neither var injected",
			base:  nil,
			hints: &model.RuntimeHints{InjectBVBRCToken: true, StagerOverrides: &model.StagerOverrides{HTTPCredential: &model.HTTPCredential{Token: ""}}},
			want:  nil,
		},
		{
			name:  "hint set but no StagerOverrides: neither var injected",
			base:  nil,
			hints: &model.RuntimeHints{InjectBVBRCToken: true},
			want:  nil,
		},
		{
			name:  "nil hints: passthrough",
			base:  map[string]string{"FOO": "bar"},
			hints: nil,
			want:  map[string]string{"FOO": "bar"},
		},
		{
			name:  "hint set with token: existing base vars preserved",
			base:  map[string]string{"FOO": "bar"},
			hints: &model.RuntimeHints{InjectBVBRCToken: true, StagerOverrides: &model.StagerOverrides{HTTPCredential: cred}},
			want:  map[string]string{"FOO": "bar", "BVBRC_TOKEN": "secret-token-value", "KB_AUTH_TOKEN": "secret-token-value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectBVBRCTokenEnv(tt.base, tt.hints)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestInjectBVBRCTokenEnv_DoesNotMutateSharedMap guards against a shared
// secrets map (e.g. the worker's own w.secrets, injected via --secret) being
// mutated in place by a per-task token injection.
func TestInjectBVBRCTokenEnv_DoesNotMutateSharedMap(t *testing.T) {
	shared := map[string]string{"HF_TOKEN": "hf_abc"}
	hints := &model.RuntimeHints{
		InjectBVBRCToken: true,
		StagerOverrides:  &model.StagerOverrides{HTTPCredential: &model.HTTPCredential{Token: "secret-token-value"}},
	}

	got := injectBVBRCTokenEnv(shared, hints)

	if len(shared) != 1 {
		t.Fatalf("shared map was mutated: %v", shared)
	}
	if _, ok := shared["BVBRC_TOKEN"]; ok {
		t.Error("shared map leaked BVBRC_TOKEN")
	}
	if got["BVBRC_TOKEN"] != "secret-token-value" {
		t.Errorf("returned map missing injected token: %v", got)
	}
}
