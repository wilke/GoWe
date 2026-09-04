package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseLabelFlags(t *testing.T) {
	tests := []struct {
		name    string
		raw     []string
		want    map[string]string
		wantErr string
	}{
		{"nil input", nil, map[string]string{}, ""},
		{"empty input", []string{}, map[string]string{}, ""},
		{"single pair", []string{"project=demo"}, map[string]string{"project": "demo"}, ""},
		{
			"multiple pairs",
			[]string{"project=demo", "env=prod"},
			map[string]string{"project": "demo", "env": "prod"},
			"",
		},
		{
			"value contains equals sign",
			[]string{"query=a=b=c"},
			map[string]string{"query": "a=b=c"},
			"",
		},
		{
			"empty value is allowed",
			[]string{"note="},
			map[string]string{"note": ""},
			"",
		},
		{
			"later duplicate key wins",
			[]string{"env=dev", "env=prod"},
			map[string]string{"env": "prod"},
			"",
		},
		{"missing equals sign", []string{"project"}, nil, `invalid --label "project"`},
		{"empty key", []string{"=value"}, nil, `invalid --label "=value"`},
		{"empty entry", []string{""}, nil, `invalid --label ""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLabelFlags(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseLabelFlags_ReservedKeysOverriddenByOwnFlags documents the merge
// order in newSubmitCmd: --label entries are parsed first, then the implicit
// worker_group/debug labels are applied on top, so they win on collision.
// This test only exercises the parser itself (the merge happens in RunE);
// it exists to pin the documented behavior that --label worker_group=x is a
// legal (if overridden) input, not a parse error.
func TestParseLabelFlags_ReservedKeysOverriddenByOwnFlags(t *testing.T) {
	got, err := parseLabelFlags([]string{"worker_group=ignored", "debug=ignored"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"worker_group": "ignored", "debug": "ignored"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
