package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func TestParseSecretFlags(t *testing.T) {
	tests := []struct {
		name    string
		file    string // file contents; "" means no --secret-file
		flags   []string
		want    map[string]string
		wantErr string
	}{
		{"neither set", "", nil, nil, ""},
		{"flags only", "", []string{"A=1", "B=2"}, map[string]string{"A": "1", "B": "2"}, ""},
		{"file only", "A=from-file\nB=also-file\n", nil, map[string]string{"A": "from-file", "B": "also-file"}, ""},
		{
			"flags win over file on collision",
			"A=from-file\nB=from-file\n",
			[]string{"A=from-flag"},
			map[string]string{"A": "from-flag", "B": "from-file"},
			"",
		},
		{
			"file comments and blank lines skipped",
			"# a comment\n\nA=1\n  # indented comment\nB=2\n",
			nil,
			map[string]string{"A": "1", "B": "2"},
			"",
		},
		{"invalid flag: no equals", "", []string{"NOTVALID"}, nil, `invalid --secret`},
		{"invalid flag: empty name", "", []string{"=value"}, nil, `invalid --secret`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := ""
			if tt.file != "" {
				dir := t.TempDir()
				file = filepath.Join(dir, "secrets.env")
				if err := os.WriteFile(file, []byte(tt.file), 0o600); err != nil {
					t.Fatalf("write secret file: %v", err)
				}
			}
			got, err := parseSecretFlags(file, tt.flags)
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

// TestParseSecretFlags_ErrorNeverPrintsRawValue is the #260 L19 regression
// test: a mistyped "--secret value" (no "=") must not have its raw text —
// which the user likely intended as a secret VALUE, not a name — appear in
// the error message.
func TestParseSecretFlags_ErrorNeverPrintsRawValue(t *testing.T) {
	const mistypedValue = "super-secret-value-mistyped-as-a-name"
	_, err := parseSecretFlags("", []string{mistypedValue})
	if err == nil {
		t.Fatal("expected an error for a --secret entry with no '='")
	}
	if strings.Contains(err.Error(), mistypedValue) {
		t.Errorf("error = %q, must not contain the raw --secret entry", err.Error())
	}
	if !strings.Contains(err.Error(), "entry #1") {
		t.Errorf("error = %q, want it to name the entry position", err.Error())
	}
}

// TestSubmitCommand_SecretsNotEchoedAndDelivered submits with both
// --secret-file and an overriding --secret flag, and verifies: (1) the CLI's
// captured stdout never contains the secret value, and (2) the server
// actually received and stored the merged secrets (flag value wins).
func TestSubmitCommand_SecretsNotEchoedAndDelivered(t *testing.T) {
	url, st := startTestServerWithStore(t)

	// Hermetic auth: the CLI takes its Bearer token ONLY from
	// $HOME/.gowe/credentials.json (LoadToken in login.go). Without an
	// isolated HOME holding a synthetic token, the test silently uses the
	// developer's real credentials — and on a runner with none it submits
	// anonymously, which the server refuses for submissions carrying
	// secrets (403). The test server has no token verifier, so a synthetic
	// pipe-format token authenticates as "cli-tester".
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gowe"), 0o700); err != nil {
		t.Fatalf("mkdir .gowe: %v", err)
	}
	creds := []byte(`{"token":"un=cli-tester|tokenid=cli-t1|expiry=4102444800|sig=x"}`)
	if err := os.WriteFile(filepath.Join(home, ".gowe", "credentials.json"), creds, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(secretFile, []byte("HF_TOKEN=file-value-should-be-overridden\n# comment\nAPI_KEY=file-api-key\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	output, err := runCLI(t,
		"--server", url,
		"submit", testdataPath("separate/pipeline.cwl"),
		"--inputs", testdataPath("separate/job.yml"),
		"--secret-file", secretFile,
		"--secret", "HF_TOKEN=flag-value-wins-abc123",
		"--secrets-retention", "on_terminal",
	)

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	stdout := buf.String()

	if err != nil {
		t.Fatalf("submit error: %v\noutput: %s\nstdout: %s", err, output, stdout)
	}

	for _, leaked := range []string{"flag-value-wins-abc123", "file-value-should-be-overridden", "file-api-key"} {
		if strings.Contains(output, leaked) || strings.Contains(stdout, leaked) {
			t.Errorf("CLI output leaked a secret value %q\ncommand output: %s\nstdout: %s", leaked, output, stdout)
		}
	}

	// Find the created submission and confirm the server actually has the
	// merged secrets (flag wins on the HF_TOKEN collision).
	ctx := context.Background()
	subs, _, err := st.ListSubmissions(ctx, model.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(subs) == 0 {
		t.Fatalf("no submissions found")
	}
	sub, err := st.GetSubmission(ctx, subs[0].ID)
	if err != nil || sub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if sub.Secrets["HF_TOKEN"] != "flag-value-wins-abc123" {
		t.Errorf("stored HF_TOKEN = %q, want flag-value-wins-abc123 (flag must win over file)", sub.Secrets["HF_TOKEN"])
	}
	if sub.Secrets["API_KEY"] != "file-api-key" {
		t.Errorf("stored API_KEY = %q, want file-api-key", sub.Secrets["API_KEY"])
	}
	if sub.SecretsRetention != "on_terminal" {
		t.Errorf("SecretsRetention = %q, want on_terminal", sub.SecretsRetention)
	}
}
