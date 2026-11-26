package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeSvcToken is the token the fake server issues.
const fakeSvcToken = "gowe_svc_FAKEfakeFAKEfakeFAKEfakeFAKEfakeFAKEfake123"

// svcTokenReq is a request the fake service-token API received.
type svcTokenReq struct {
	Method, Path, Auth string
	Body               map[string]any
}

// serviceTokensServer fakes the service-token admin API and records
// requests, with their Authorization header.
func serviceTokensServer(t *testing.T) (*httptest.Server, func() []svcTokenReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []svcTokenReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		reqs = append(reqs, svcTokenReq{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		write := func(data string) {
			_, _ = io.WriteString(w, `{"status":"ok","request_id":"r","timestamp":"2026-01-01T00:00:00Z","data":`+data+`}`)
		}
		switch p := r.URL.Path; {
		case r.Method == "POST" && p == "/api/v1/admin/service-tokens":
			w.WriteHeader(http.StatusCreated)
			write(`{"id":"svt_new","name":"model-router","username":"model-router@service.gowe","token":"` + fakeSvcToken +
				`","token_prefix":"gowe_svc_FAKEfak","created_at":"2026-01-01T00:00:00Z","expires_at":"2026-04-01T00:00:00Z"}`)
		case r.Method == "GET" && p == "/api/v1/admin/service-tokens":
			write(`{"total":1,"service_tokens":[{"id":"svt_1","name":"model-router","username":"model-router@service.gowe","token_prefix":"gowe_svc_abcdefg","created_at":"2026-01-01T00:00:00Z","expires_at":null,"last_used_at":"2026-01-02T00:00:00Z","expired":false}]}`)
		case r.Method == "DELETE" && strings.HasPrefix(p, "/api/v1/admin/service-tokens/"):
			write(`{"id":"svt_1","revoked":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":"error","request_id":"r","timestamp":"2026-01-01T00:00:00Z","error":{"code":"NOT_FOUND","message":"no route"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []svcTokenReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]svcTokenReq(nil), reqs...)
	}
}

// create writes the token to a new 0600 file, prints the id, username,
// prefix, expiry and file but never the token, and refuses an existing file.
func TestAdminServiceTokens_Create(t *testing.T) {
	srv, reqs := serviceTokensServer(t)
	path := filepath.Join(t.TempDir(), "router.token")
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "service-tokens", "create", "model-router",
		"--description", "router", "--expires", "2160h", "--out", path)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if strings.Contains(out, fakeSvcToken) {
		t.Errorf("the token was printed:\n%s", out)
	}
	for _, want := range []string{"svt_new", "model-router@service.gowe", "gowe_svc_FAKEfak", "2026-04-01T00:00:00Z", path} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "POST" || last.Path != "/api/v1/admin/service-tokens" || last.Body["name"] != "model-router" ||
		last.Body["description"] != "router" || last.Body["expires_in"] != "2160h" {
		t.Errorf("create request: %+v", last)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != fakeSvcToken+"\n" {
		t.Fatalf("token file = %q, %v", data, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}

	// An existing file is never overwritten, and no token is created.
	n := len(reqs())
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "service-tokens", "create", "model-router", "--out", path); err == nil {
		t.Error("create over an existing file must fail")
	}
	if len(reqs()) != n {
		t.Error("no request may be made when the output file exists")
	}
	if data, _ := os.ReadFile(path); string(data) != fakeSvcToken+"\n" {
		t.Errorf("the existing file was changed: %q", data)
	}
}

// Invalid arguments are refused before any request: --out is required.
func TestAdminServiceTokens_CreateValidation(t *testing.T) {
	srv, reqs := serviceTokensServer(t)
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no --out", []string{"create", "model-router"}, "--out is required"},
		{"bad name", []string{"create", "Model-Router", "--out", filepath.Join(dir, "a")}, "service name"},
		{"bad expiry", []string{"create", "r", "--expires", "soon", "--out", filepath.Join(dir, "b")}, "--expires takes"},
		{"negative expiry", []string{"create", "r", "--expires=-1h", "--out", filepath.Join(dir, "c")}, "--expires takes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := append([]string{"--server", srv.URL, "admin", "service-tokens"}, c.args...)
			_, err := runBlessCLI(t, "", args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
	if len(reqs()) != 0 {
		t.Errorf("requests made for invalid arguments: %+v", reqs())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files left behind: %v", entries)
	}
}

// With --debug the token is written to the file only: not to the debug log,
// nor to stdout.
func TestAdminServiceTokens_CreateDebugDoesNotLogToken(t *testing.T) {
	srv, _ := serviceTokensServer(t)
	path := filepath.Join(t.TempDir(), "router.token")
	out, stdio, err := runCLIWithStdio(t, "--debug", "--server", srv.URL, "admin", "service-tokens", "create", "model-router", "--out", path)
	if err != nil {
		t.Fatalf("create --debug: %v\n%s\n%s", err, out, stdio)
	}
	if !strings.Contains(stdio, "level=DEBUG") || !strings.Contains(stdio, "HTTP response") {
		t.Fatalf("debug logging must be on (the test checks its output):\n%s", stdio)
	}
	if strings.Contains(stdio, fakeSvcToken) || strings.Contains(out, fakeSvcToken) {
		t.Errorf("the token reached the log or the output:\n%s\n%s", stdio, out)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != fakeSvcToken+"\n" {
		t.Errorf("token file = %q, %v", data, err)
	}
}

// A failed write removes the file and names the token to revoke.
func TestAdminServiceTokens_CreateWriteFailure(t *testing.T) {
	srv, _ := serviceTokensServer(t)
	old := writeSecretFile
	writeSecretFile = func(f *os.File, _ string) error {
		f.Close()
		return errors.New("disk full")
	}
	t.Cleanup(func() { writeSecretFile = old })
	path := filepath.Join(t.TempDir(), "router.token")
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "service-tokens", "create", "model-router", "--out", path)
	if err == nil || !strings.Contains(err.Error(), "revoke svt_new") {
		t.Fatalf("err = %v, want the revoke hint", err)
	}
	if strings.Contains(out+err.Error(), fakeSvcToken) {
		t.Errorf("the token was printed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the file was not removed: %v", statErr)
	}
}

func TestAdminServiceTokens_ListAndRevoke(t *testing.T) {
	srv, reqs := serviceTokensServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "service-tokens", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"ID", "USERNAME", "LAST USED", "svt_1", "model-router@service.gowe", "gowe_svc_abcdefg", "never", "2026-01-02T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
	out, err = runBlessCLI(t, "", "--server", srv.URL, "admin", "service-tokens", "revoke", "svt_1")
	if err != nil || !strings.Contains(out, "Revoked service token svt_1") {
		t.Fatalf("revoke: %v\n%s", err, out)
	}
	if last := reqs()[len(reqs())-1]; last.Method != "DELETE" || last.Path != "/api/v1/admin/service-tokens/svt_1" {
		t.Errorf("revoke request: %+v", last)
	}
}

// --token-file is sent as the bearer token, ahead of the stored
// credentials; a file readable by group or others, or not a regular file, is
// refused before any request.
func TestTokenFile(t *testing.T) {
	srv, reqs := serviceTokensServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gowe"), 0o700); err != nil {
		t.Fatal(err)
	}
	stored := "un=someone|tokenid=x|expiry=4102444800|sig=y"
	if err := os.WriteFile(filepath.Join(home, ".gowe", "credentials.json"), []byte(`{"token":"`+stored+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Without --token-file: the stored credentials.
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "service-tokens", "list"); err != nil {
		t.Fatal(err)
	}
	if got := reqs()[len(reqs())-1].Auth; got != "Bearer "+stored {
		t.Fatalf("Authorization without --token-file = %q, want the stored token", got)
	}

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "router.token")
	if err := os.WriteFile(tokenFile, []byte("  "+fakeSvcToken+" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runBlessCLI(t, "", "--token-file", tokenFile, "--server", srv.URL, "admin", "service-tokens", "list"); err != nil {
		t.Fatalf("--token-file: %v\n%s", err, out)
	}
	if got := reqs()[len(reqs())-1].Auth; got != "Bearer "+fakeSvcToken {
		t.Errorf("Authorization with --token-file = %q, want the trimmed file content", got)
	}

	n := len(reqs())
	wide := filepath.Join(dir, "wide.token")
	if err := os.WriteFile(wide, []byte(fakeSvcToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o640); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.token")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symbolic link is never followed, even to a valid token file.
	link := filepath.Join(dir, "link.token")
	if err := os.Symlink(tokenFile, link); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(dir, "dirlink.token")
	if err := os.Symlink(dir, dirLink); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling.token")
	if err := os.Symlink(filepath.Join(dir, "nope"), dangling); err != nil {
		t.Fatal(err)
	}
	// At most 4096 bytes.
	large := filepath.Join(dir, "large.token")
	if err := os.WriteFile(large, []byte(fakeSvcToken+strings.Repeat(" ", 4097-len(fakeSvcToken))), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, path, want string }{
		{"mode 0640", wide, "0600 or stricter"},
		{"directory", dir, "not a regular file"},
		{"missing", filepath.Join(dir, "nope"), "--token-file"},
		{"empty", empty, "is empty"},
		{"symlink to a valid token file", link, "symbolic link"},
		{"symlink to a directory", dirLink, "symbolic link"},
		{"dangling symlink", dangling, "symbolic link"},
		{"larger than 4096 bytes", large, "larger than 4096 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := runBlessCLI(t, "", "--token-file", c.path, "--server", srv.URL, "admin", "service-tokens", "list")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to contain %q", err, c.want)
			}
			if strings.Contains(out, fakeSvcToken) || (err != nil && strings.Contains(err.Error(), fakeSvcToken)) {
				t.Errorf("the token content was printed")
			}
		})
	}
	if len(reqs()) != n {
		t.Errorf("requests made with a refused token file: %+v", reqs()[n:])
	}

	// Exactly 4096 bytes is accepted.
	limit := filepath.Join(dir, "limit.token")
	if err := os.WriteFile(limit, []byte(fakeSvcToken+strings.Repeat("\n", 4096-len(fakeSvcToken))), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runBlessCLI(t, "", "--token-file", limit, "--server", srv.URL, "admin", "service-tokens", "list"); err != nil {
		t.Fatalf("a 4096-byte token file: %v\n%s", err, out)
	}
	if got := reqs()[len(reqs())-1].Auth; got != "Bearer "+fakeSvcToken {
		t.Errorf("Authorization with a 4096-byte token file = %q", got)
	}
}

// A symbolic link at the --out target is refused (the file is created with
// O_EXCL, which does not follow a final symlink), whether it dangles or
// points to an existing file: no request is made, the link and its target
// are left as they were.
func TestAdminServiceTokens_CreateRefusesSymlinkOut(t *testing.T) {
	srv, reqs := serviceTokensServer(t)
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, target string }{
		{"dangling", filepath.Join(dir, "not-yet")},
		{"to an existing file", existing},
	} {
		t.Run(c.name, func(t *testing.T) {
			link := filepath.Join(dir, "out-"+strings.ReplaceAll(c.name, " ", "-"))
			if err := os.Symlink(c.target, link); err != nil {
				t.Fatal(err)
			}
			n := len(reqs())
			out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "service-tokens", "create", "model-router", "--out", link)
			if err == nil || !strings.Contains(err.Error(), "--out") {
				t.Fatalf("create with a symlink --out: err = %v, want the --out refusal\n%s", err, out)
			}
			if len(reqs()) != n {
				t.Error("no request may be made when --out is a symlink")
			}
			if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("the link was replaced or removed: %v %v", fi, err)
			}
			if c.target == existing {
				if data, _ := os.ReadFile(existing); string(data) != "keep\n" {
					t.Errorf("the link target was changed: %q", data)
				}
			} else if _, err := os.Lstat(c.target); !os.IsNotExist(err) {
				t.Errorf("the dangling link's target was created: %v", err)
			}
		})
	}
}

// A service token is never parsed as a BV-BRC token nor sent to the
// Workspace by --workspace-upload.
func TestUploadInputsToWorkspace_RefusesServiceToken(t *testing.T) {
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the Workspace was contacted: %s %s", r.Method, r.URL.Path)
	}))
	defer ws.Close()
	_, err := uploadInputsToWorkspace(map[string]any{}, fakeSvcToken, ws.URL)
	if err == nil || !strings.Contains(err.Error(), "service token") {
		t.Fatalf("err = %v, want the service token refusal", err)
	}
}
