package staging

import (
	"testing"
)

func TestParseWorkspaceURI(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		want    string
		wantErr bool
	}{
		{
			name: "standard triple-slash",
			uri:  "ws:///awilke@bvbrc/home/test.fasta",
			want: "/awilke@bvbrc/home/test.fasta",
		},
		{
			name: "double-slash",
			uri:  "ws://awilke@bvbrc/home/test.fasta",
			want: "/awilke@bvbrc/home/test.fasta",
		},
		{
			name: "nested path",
			uri:  "ws:///user@bvbrc/home/results/output/file.txt",
			want: "/user@bvbrc/home/results/output/file.txt",
		},
		{
			name:    "wrong scheme",
			uri:     "http://example.com/file",
			wantErr: true,
		},
		{
			name:    "empty path",
			uri:     "ws:///",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWorkspaceURI(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseWorkspaceURI(%q) error = %v, wantErr %v", tt.uri, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseWorkspaceURI(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

func TestWorkspaceStager_Supports(t *testing.T) {
	s := NewWorkspaceStager(WorkspaceConfig{}, nil)

	if !s.Supports("ws") {
		t.Error("expected Supports(ws) = true")
	}
	if s.Supports("file") {
		t.Error("expected Supports(file) = false")
	}
	if s.Supports("http") {
		t.Error("expected Supports(http) = false")
	}
}

func TestWorkspaceStager_WithToken(t *testing.T) {
	original := NewWorkspaceStager(WorkspaceConfig{Token: "original"}, nil)
	cloned := original.WithToken("cloned-token")

	if original.config.Token != "original" {
		t.Errorf("original token changed to %q", original.config.Token)
	}
	if cloned.config.Token != "cloned-token" {
		t.Errorf("cloned token = %q, want %q", cloned.config.Token, "cloned-token")
	}
}

func TestWorkspaceStager_ResolveToken(t *testing.T) {
	s := NewWorkspaceStager(WorkspaceConfig{Token: "config-token"}, nil)

	tests := []struct {
		name string
		opts StageOptions
		want string
	}{
		{
			name: "opts.Token wins",
			opts: StageOptions{
				Token:       "opts-token",
				Credentials: &CredentialSet{Token: "cred-token"},
			},
			want: "opts-token",
		},
		{
			name: "credentials.Token second",
			opts: StageOptions{
				Credentials: &CredentialSet{Token: "cred-token"},
			},
			want: "cred-token",
		},
		{
			name: "config.Token fallback",
			opts: StageOptions{},
			want: "config-token",
		},
		{
			name: "empty credentials falls through",
			opts: StageOptions{
				Credentials: &CredentialSet{},
			},
			want: "config-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.resolveToken(tt.opts)
			if got != tt.want {
				t.Errorf("resolveToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkspaceStager_Defaults(t *testing.T) {
	s := NewWorkspaceStager(WorkspaceConfig{}, nil)

	if s.config.WorkspaceURL != "https://p3.theseed.org/services/Workspace" {
		t.Errorf("default WorkspaceURL = %q", s.config.WorkspaceURL)
	}
	if s.config.Timeout != 5*60*1e9 { // 5 minutes in nanoseconds
		t.Errorf("default Timeout = %v", s.config.Timeout)
	}
	if s.config.MaxRetries != 3 {
		t.Errorf("default MaxRetries = %d", s.config.MaxRetries)
	}
}
