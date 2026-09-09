package main

import "testing"

func TestNormalizeBasePath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "empty is unset", in: "", want: ""},
		{name: "trims trailing slash", in: "/a/b/", want: "/a/b"},
		{name: "already normalized", in: "/a/b", want: "/a/b"},
		{name: "single segment", in: "/gowe", want: "/gowe"},
		{name: "no leading slash is an error", in: "a/b", wantErr: true},
		{name: "root alone is an error", in: "/", wantErr: true},
		{name: "root with extra slashes is an error", in: "//", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeBasePath(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeBasePath(%q) = %q, nil; want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeBasePath(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeBasePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
