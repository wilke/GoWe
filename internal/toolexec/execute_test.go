package toolexec

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveApptainerImage(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		imageDir string
		want     string
	}{
		{
			name:  "registry image gets docker prefix",
			image: "dxkb/esmfold:latest",
			want:  "docker://dxkb/esmfold:latest",
		},
		{
			name:  "registry image with no tag",
			image: "ubuntu",
			want:  "docker://ubuntu",
		},
		{
			name:  "absolute sif path used as-is",
			image: "/scout/containers/all.sif",
			want:  "/scout/containers/all.sif",
		},
		{
			name:     "absolute sif ignores imageDir",
			image:    "/scout/containers/all.sif",
			imageDir: "/other/dir",
			want:     "/scout/containers/all.sif",
		},
		{
			name:     "relative sif resolved against imageDir",
			image:    "all.sif",
			imageDir: "/scout/containers",
			want:     "/scout/containers/all.sif",
		},
		{
			name:     "relative sif with subdirectory",
			image:    "gpu/predict.sif",
			imageDir: "/scout/containers",
			want:     "/scout/containers/gpu/predict.sif",
		},
		{
			name:  "relative sif without imageDir passed through",
			image: "all.sif",
			want:  "all.sif",
		},
		{
			name:     "registry image ignores imageDir",
			image:    "dxkb/esmfold:latest",
			imageDir: "/scout/containers",
			want:     "docker://dxkb/esmfold:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveApptainerImage(tt.image, tt.imageDir)
			if got != tt.want {
				t.Errorf("resolveApptainerImage(%q, %q) = %q, want %q", tt.image, tt.imageDir, got, tt.want)
			}
		})
	}
}

func TestTailString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{
			name:  "empty buffer",
			input: "",
			limit: 100,
			want:  "",
		},
		{
			name:  "under limit returns full content",
			input: "hello world",
			limit: 100,
			want:  "hello world",
		},
		{
			name:  "exactly at limit returns full content",
			input: "12345",
			limit: 5,
			want:  "12345",
		},
		{
			name:  "over limit truncates with marker",
			input: "abcdefghij",
			limit: 5,
			want:  "... [truncated] ...\nfghij",
		},
		{
			name:  "large content keeps tail",
			input: strings.Repeat("x", 1000) + "TAIL",
			limit: 10,
			want:  "... [truncated] ...\nxxxxxxTAIL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := bytes.NewBufferString(tt.input)
			got := tailString(buf, tt.limit)
			if got != tt.want {
				t.Errorf("tailString(%d bytes, limit=%d) = %q, want %q",
					len(tt.input), tt.limit, got, tt.want)
			}
		})
	}
}
