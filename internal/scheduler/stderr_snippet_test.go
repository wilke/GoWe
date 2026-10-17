package scheduler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStderrSnippet(t *testing.T) {
	traceback := "execute: apptainer command failed (exit 1): [transformers] PyTorch was not found.\n" +
		"Traceback (most recent call last):\n" +
		strings.Repeat("  File \"/usr/local/lib/python3.12/site-packages/x.py\", line 1, in f\n    f()\n", 40) +
		"elastic_transport.ConnectionError: Cannot connect to host localhost:24043\n" +
		"Unclosed client session\n"

	tests := []struct {
		name      string
		in        string
		max       int
		wantSame  bool
		contains  []string
		notPrefix string
	}{
		{name: "empty", in: "", max: 1000, wantSame: true},
		{name: "short unchanged", in: "boom\n", max: 1000, wantSame: true},
		{name: "exactly max unchanged", in: strings.Repeat("a", 1000), max: 1000, wantSame: true},
		{
			name: "traceback keeps first line and final error",
			in:   traceback,
			max:  1000,
			contains: []string{
				"execute: apptainer command failed (exit 1)",
				"...(truncated)...",
				"elastic_transport.ConnectionError: Cannot connect to host localhost:24043",
				"Unclosed client session",
			},
		},
		{
			name:     "single long line",
			in:       strings.Repeat("x", 5000) + "END",
			max:      1000,
			contains: []string{"...(truncated)...", "END"},
		},
		{
			name:     "multibyte runes are not split",
			in:       strings.Repeat("é", 300) + "\n" + strings.Repeat("ü", 2000) + "\nfinal",
			max:      1000,
			contains: []string{"final"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stderrSnippet(tc.in, tc.max)
			if tc.wantSame {
				if got != tc.in {
					t.Fatalf("got %q, want input unchanged", got)
				}
				return
			}
			if !utf8.ValidString(got) {
				t.Fatalf("snippet is not valid UTF-8: %q", got)
			}
			if limit := tc.max + len(snippetMarker) + utf8.UTFMax; len(got) > limit {
				t.Fatalf("len %d > %d", len(got), limit)
			}
			for _, c := range tc.contains {
				if !strings.Contains(got, c) {
					t.Errorf("snippet lacks %q:\n%s", c, got)
				}
			}
		})
	}
}
