package cwl

import "testing"

func TestParseLocationScheme(t *testing.T) {
	tests := []struct {
		input      string
		wantScheme string
		wantPath   string
	}{
		// Workspace scheme.
		{"ws:///user@bvbrc/home/output/", "ws", "/user@bvbrc/home/output/"},
		{"ws:///awilke@bvbrc/home/gowe-test", "ws", "/awilke@bvbrc/home/gowe-test"},

		// File scheme.
		{"file:///data/output/", "file", "/data/output/"},
		{"file:///tmp/work", "file", "/tmp/work"},

		// Shock scheme.
		{"shock://p3.theseed.org/services/shock_api/node/abc123", "shock", "p3.theseed.org/services/shock_api/node/abc123"},

		// HTTPS scheme.
		{"https://example.com/data/", "https", "example.com/data/"},

		// HTTP scheme.
		{"http://example.com/data/", "http", "example.com/data/"},

		// Bare strings (no scheme).
		{"/user@bvbrc/home/output/", "", "/user@bvbrc/home/output/"},
		{"/tmp/local/path", "", "/tmp/local/path"},
		{"relative/path", "", "relative/path"},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			scheme, path := ParseLocationScheme(tt.input)
			if scheme != tt.wantScheme {
				t.Errorf("scheme = %q, want %q", scheme, tt.wantScheme)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

func TestBuildLocation(t *testing.T) {
	tests := []struct {
		scheme string
		path   string
		want   string
	}{
		{"ws", "/user@bvbrc/home/output/", "ws:///user@bvbrc/home/output/"},
		{"file", "/data/output/", "file:///data/output/"},
		{"shock", "p3.theseed.org/node/abc", "shock://p3.theseed.org/node/abc"},
		{"https", "example.com/data/", "https://example.com/data/"},
	}

	for _, tt := range tests {
		t.Run(tt.scheme+":"+tt.path, func(t *testing.T) {
			got := BuildLocation(tt.scheme, tt.path)
			if got != tt.want {
				t.Errorf("BuildLocation(%q, %q) = %q, want %q", tt.scheme, tt.path, got, tt.want)
			}
		})
	}
}

func TestInferScheme(t *testing.T) {
	tests := []struct {
		execType string
		want     string
	}{
		{"bvbrc", SchemeWorkspace},
		{"container", SchemeFile},
		{"local", SchemeFile},
		{"unknown", SchemeFile},
		{"", SchemeFile},
	}

	for _, tt := range tests {
		t.Run(tt.execType, func(t *testing.T) {
			got := InferScheme(tt.execType)
			if got != tt.want {
				t.Errorf("InferScheme(%q) = %q, want %q", tt.execType, got, tt.want)
			}
		})
	}
}

func TestParseLocationScheme_RoundTrip(t *testing.T) {
	// Ensure BuildLocation → ParseLocationScheme round-trips correctly.
	cases := []struct {
		scheme string
		path   string
	}{
		{"ws", "/user@bvbrc/home/output/"},
		{"file", "/tmp/data"},
	}

	for _, tc := range cases {
		uri := BuildLocation(tc.scheme, tc.path)
		gotScheme, gotPath := ParseLocationScheme(uri)
		if gotScheme != tc.scheme {
			t.Errorf("round-trip scheme: got %q, want %q (uri=%q)", gotScheme, tc.scheme, uri)
		}
		if gotPath != tc.path {
			t.Errorf("round-trip path: got %q, want %q (uri=%q)", gotPath, tc.path, uri)
		}
	}
}
