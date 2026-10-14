package toolexec

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStderrTail(t *testing.T) {
	secret := "s3cr3t-value-0123456789abcdef"
	secrets := map[string]string{"TOKEN": secret}
	pad := strings.Repeat("x", maxStderrTail)

	tests := []struct {
		name   string
		stderr string
		want   string // exact result, if set
		check  func(t *testing.T, got string)
	}{
		{name: "empty", stderr: "  \n", want: ""},
		{name: "short is kept whole and trimmed", stderr: "\nboom\n", want: "boom"},
		{name: "short secret is masked", stderr: "token " + secret, want: "token ***REDACTED***"},
		{
			name: "secret straddling the cut leaves no fragment",
			// The cut (2048 bytes from the end) falls inside the secret.
			stderr: secret + strings.Repeat("y", maxStderrTail-len(secret)/2),
			check: func(t *testing.T, got string) {
				for i := 4; i <= len(secret); i++ {
					if strings.Contains(got, secret[len(secret)-i:]) {
						t.Fatalf("tail leaks the end of the secret (%d bytes): %q", i, got[:40])
					}
				}
			},
		},
		{
			name: "cut never splits a rune",
			// "é" is 2 bytes; an odd-length ASCII suffix puts the byte cut
			// in the middle of one.
			stderr: strings.Repeat("é", maxStderrTail) + "z",
			check: func(t *testing.T, got string) {
				if !utf8.ValidString(got) {
					t.Fatalf("tail is not valid UTF-8")
				}
				if !strings.HasPrefix(got, "...") || len(got)-3 > maxStderrTail {
					t.Fatalf("tail not bounded: %d bytes", len(got))
				}
			},
		},
		{
			name:   "long output keeps the end",
			stderr: pad + "\nlast line",
			check: func(t *testing.T, got string) {
				if !strings.HasSuffix(got, "last line") || len(got) != 3+maxStderrTail {
					t.Fatalf("got %d bytes, suffix %q", len(got), got[len(got)-9:])
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stderrTail(tt.stderr, secrets)
			if tt.check != nil {
				tt.check(t, got)
				return
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCollectOutputsError(t *testing.T) {
	base := errors.New("output \"o\": required output missing")
	if err := collectOutputsError(base, "", nil); !errors.Is(err, base) || strings.Contains(err.Error(), "stderr") {
		t.Fatalf("empty stderr: %v", err)
	}
	err := collectOutputsError(base, "tool said no", nil)
	if !errors.Is(err, base) || !strings.HasSuffix(err.Error(), "tool stderr (tail):\ntool said no") {
		t.Fatalf("with stderr: %v", err)
	}
}
