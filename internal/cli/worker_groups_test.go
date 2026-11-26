package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRawKey is the raw key the fake server mints.
const fakeRawKey = "gwk_fake-raw-key-0123456789"

// managedGroupsServer fakes the worker group manager API and records
// requests (the path as sent, escaped).
func managedGroupsServer(t *testing.T) (*httptest.Server, func() []recordedReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedReq
	rule := `{"group":"gpu","users":["alice","bob"],"managers":["mgr"],"admins":false,"updated_by":"mgr","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		reqs = append(reqs, recordedReq{r.Method, r.URL.RequestURI(), body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		write := func(data string) {
			_, _ = io.WriteString(w, `{"status":"ok","request_id":"r","timestamp":"2026-01-01T00:00:00Z","data":`+data+`}`)
		}
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == "/api/v1/worker-groups":
			write(`{"groups":[` + rule + `]}`)
		case r.Method == "GET" && p == "/api/v1/worker-groups/gpu":
			write(rule)
		case (r.Method == "POST" || r.Method == "DELETE") && strings.HasPrefix(p, "/api/v1/worker-groups/gpu/users"):
			write(rule)
		case r.Method == "GET" && p == "/api/v1/worker-groups/gpu/keys":
			write(`{"total":1,"worker_keys":[{"id":"wk_1","label":"node1","key_prefix":"gwk_abc","groups":["gpu"],"disabled":false,"created_by":"mgr","created_at":"2026-01-01T00:00:00Z"}]}`)
		case r.Method == "POST" && p == "/api/v1/worker-groups/gpu/keys":
			w.WriteHeader(http.StatusCreated)
			write(`{"key":"` + fakeRawKey + `","worker_key":{"id":"wk_new","label":"n","key_prefix":"gwk_fake-ra","groups":["gpu"],"created_by":"mgr"},"warning":"store this key now"}`)
		case r.Method == "DELETE" && strings.HasPrefix(p, "/api/v1/worker-groups/gpu/keys/"):
			write(`{"id":"wk_1","revoked":true}`)
		case r.Method == "POST" && p == "/api/v1/worker-groups/garbled/keys":
			// 201, but data the CLI cannot read as a minted key.
			w.WriteHeader(http.StatusCreated)
			write(`"not a minted key"`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":"error","request_id":"r","timestamp":"2026-01-01T00:00:00Z","error":{"code":"NOT_FOUND","message":"no route"}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedReq(nil), reqs...)
	}
}

func TestWorkerGroups_ListShowUsers(t *testing.T) {
	srv, reqs := managedGroupsServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"GROUP", "MANAGERS", "gpu", "alice,bob", "mgr"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
	out, err = runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "show", "gpu")
	if err != nil || !strings.Contains(out, "Managers: mgr") || !strings.Contains(out, "Users:    alice, bob") {
		t.Errorf("show: %v\n%s", err, out)
	}

	if out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "add-user", "gpu", "carol"); err != nil {
		t.Fatalf("add-user: %v\n%s", err, out)
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "POST" || last.Path != "/api/v1/worker-groups/gpu/users" || last.Body["username"] != "carol" {
		t.Errorf("add-user request: %+v", last)
	}

	// The username is escaped as one path segment.
	if out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "remove-user", "gpu", "svc/robot x"); err != nil {
		t.Fatalf("remove-user: %v\n%s", err, out)
	}
	last = reqs()[len(reqs())-1]
	if last.Method != "DELETE" || last.Path != "/api/v1/worker-groups/gpu/users/svc%2Frobot%20x" {
		t.Errorf("remove-user request: %+v", last)
	}

	rs := reqs()
	if rs[0].Method != "GET" || rs[0].Path != "/api/v1/worker-groups" || rs[1].Path != "/api/v1/worker-groups/gpu" {
		t.Errorf("requests = %+v", rs)
	}
}

func TestWorkerGroups_KeysAndRevoke(t *testing.T) {
	srv, reqs := managedGroupsServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "keys", "gpu")
	if err != nil {
		t.Fatalf("keys: %v\n%s", err, out)
	}
	for _, want := range []string{"ID", "wk_1", "node1", "gwk_abc", "mgr"} {
		if !strings.Contains(out, want) {
			t.Errorf("keys output lacks %q:\n%s", want, out)
		}
	}
	if out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "revoke-key", "gpu", "wk_1"); err != nil || !strings.Contains(out, "Revoked worker key wk_1") {
		t.Fatalf("revoke-key: %v\n%s", err, out)
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "DELETE" || last.Path != "/api/v1/worker-groups/gpu/keys/wk_1" {
		t.Errorf("revoke-key request: %+v", last)
	}
}

// create-key prints the raw key once, or writes it to a new 0600 file
// without printing it.
func TestWorkerGroups_CreateKey(t *testing.T) {
	srv, reqs := managedGroupsServer(t)
	before := time.Now()
	out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "create-key", "gpu",
		"--label", "node1", "--description", "gpu box", "--expires", "720h")
	if err != nil {
		t.Fatalf("create-key: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, fakeRawKey+"\n") || !strings.Contains(out, "not shown again") {
		t.Errorf("output must start with the raw key and carry the note:\n%s", out)
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "POST" || last.Path != "/api/v1/worker-groups/gpu/keys" || last.Body["label"] != "node1" || last.Body["description"] != "gpu box" {
		t.Fatalf("create-key request: %+v", last)
	}
	if _, has := last.Body["groups"]; has {
		t.Errorf("create-key must not send groups: %+v", last.Body)
	}
	exp, err := time.Parse(time.RFC3339, last.Body["expires_at"].(string))
	if err != nil || exp.Before(before.Add(719*time.Hour)) || exp.After(time.Now().Add(721*time.Hour)) {
		t.Errorf("expires_at = %v (%v), want about 720h from now", last.Body["expires_at"], err)
	}

	// --out: a new 0600 file, the key not printed.
	path := filepath.Join(t.TempDir(), "worker.key")
	out, err = runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "create-key", "gpu", "--label", "n", "--out", path)
	if err != nil {
		t.Fatalf("create-key --out: %v\n%s", err, out)
	}
	if strings.Contains(out, fakeRawKey) {
		t.Errorf("--out must not print the key:\n%s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != fakeRawKey+"\n" {
		t.Fatalf("key file = %q, %v", data, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", fi.Mode().Perm())
	}

	// An existing file is never overwritten, and no key is created.
	n := len(reqs())
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "create-key", "gpu", "--label", "n", "--out", path); err == nil {
		t.Error("create-key over an existing file must fail")
	}
	if len(reqs()) != n {
		t.Error("no request may be made when the output file exists")
	}
	if data, _ := os.ReadFile(path); string(data) != fakeRawKey+"\n" {
		t.Errorf("the existing file was changed: %q", data)
	}

	// An RFC 3339 expiry is sent as given.
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "create-key", "gpu", "--label", "n", "--expires", "2030-01-02T03:04:05Z"); err != nil {
		t.Fatal(err)
	}
	if last := reqs()[len(reqs())-1]; last.Body["expires_at"] != "2030-01-02T03:04:05Z" {
		t.Errorf("expires_at = %v", last.Body["expires_at"])
	}
}

// runCLIWithStdio is runBlessCLI with os.Stdout and os.Stderr (where the
// logger writes) sent to a file; it returns the command's output, that
// file's content and the error.
func runCLIWithStdio(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdio")
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = f, f
	out, runErr := func() (string, error) {
		defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
		return runBlessCLI(t, "", args...)
	}()
	f.Close()
	stdio, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return out, string(stdio), runErr
}

// With --debug and --out, the raw key is written to the file only: not to
// the debug log, nor to stdout.
func TestWorkerGroups_CreateKeyDebugDoesNotLogKey(t *testing.T) {
	srv, _ := managedGroupsServer(t)
	path := filepath.Join(t.TempDir(), "worker.key")
	out, stdio, err := runCLIWithStdio(t, "--debug", "--server", srv.URL, "worker-groups", "create-key", "gpu", "--label", "n", "--out", path)
	if err != nil {
		t.Fatalf("create-key --debug --out: %v\n%s\n%s", err, out, stdio)
	}
	if !strings.Contains(stdio, "level=DEBUG") || !strings.Contains(stdio, "HTTP response") {
		t.Fatalf("debug logging must be on (the test checks its output):\n%s", stdio)
	}
	if strings.Contains(stdio, fakeRawKey) {
		t.Errorf("the raw key reached the log or stdout:\n%s", stdio)
	}
	if strings.Contains(out, fakeRawKey) {
		t.Errorf("the raw key reached the command output:\n%s", out)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != fakeRawKey+"\n" {
		t.Errorf("key file = %q, %v", data, err)
	}
}

// A failed request removes the empty --out file.
func TestWorkerGroups_CreateKeyFailureRemovesOutFile(t *testing.T) {
	srv, _ := managedGroupsServer(t)
	path := filepath.Join(t.TempDir(), "worker.key")
	_, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "create-key", "nope", "--label", "n", "--out", path)
	if err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("error = %v, want the server's error", err)
	}
	if strings.Contains(err.Error(), "may have been created") {
		t.Errorf("an error answer from the server means no key: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the --out file must be removed after a failed request: %v", statErr)
	}
}

// When the response cannot be read, or the key cannot be written to --out,
// the error says how to find and revoke the key, and --out is removed.
func TestWorkerGroups_CreateKeyAfterCreationFailures(t *testing.T) {
	srv, _ := managedGroupsServer(t)

	path := filepath.Join(t.TempDir(), "worker.key")
	out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "create-key", "garbled", "--label", "n", "--out", path)
	if err == nil {
		t.Fatal("an unreadable response must fail")
	}
	for _, want := range []string{"may have been created", "gowe worker-groups keys garbled", "gowe worker-groups revoke-key garbled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the --out file must be removed: %v", statErr)
	}
	if strings.Contains(out, fakeRawKey) {
		t.Errorf("output: %s", out)
	}

	old := writeKeyFile
	t.Cleanup(func() { writeKeyFile = old })
	writeKeyFile = func(f *os.File, key string) error {
		_, _ = f.WriteString(key[:5])
		return errors.New("disk full")
	}
	path = filepath.Join(t.TempDir(), "worker.key")
	out, err = runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "create-key", "gpu", "--label", "n", "--out", path)
	if err == nil {
		t.Fatal("a failed write must fail")
	}
	for _, want := range []string{"disk full", "wk_new", "was created", "gowe worker-groups revoke-key gpu wk_new", "gowe worker-groups keys gpu"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), fakeRawKey) || strings.Contains(out, fakeRawKey) {
		t.Errorf("the raw key must not be shown: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("the partly written --out file must be removed: %v", statErr)
	}
}

// Invalid arguments are refused before any request is made.
func TestWorkerGroups_RefusedBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"show invalid group", []string{"show", ".gpu"}, "is not a valid group name"},
		{"add-user anonymous", []string{"add-user", "gpu", "anonymous"}, `users holds "anonymous"`},
		{"add-user one arg", []string{"add-user", "gpu"}, "accepts 2 arg"},
		{"remove-user empty", []string{"remove-user", "gpu", ""}, "username is empty"},
		{"create-key without label", []string{"create-key", "gpu"}, "--label is required"},
		{"create-key bad expiry", []string{"create-key", "gpu", "--label", "n", "--expires", "soon"}, "--expires takes"},
		{"create-key negative expiry", []string{"create-key", "gpu", "--label", "n", "--expires=-1h"}, "--expires takes"},
		{"revoke-key empty id", []string{"revoke-key", "gpu", ""}, "key id is empty"},
		{"keys default-invalid group", []string{"keys", ""}, "group name is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, reqs := managedGroupsServer(t)
			_, err := runBlessCLI(t, "", append([]string{"--server", srv.URL, "worker-groups"}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if rs := reqs(); len(rs) != 0 {
				t.Errorf("no request may be made: %+v", rs)
			}
		})
	}
}

// admin worker-groups set: --manager replaces, no flag keeps (no managers
// field), --no-managers clears.
func TestAdminWorkerGroups_SetManagers(t *testing.T) {
	srv, reqs := workerGroupsServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "set", "gpu", "--user", "alice",
		"--manager", "mgr", "--manager", "mgr2", "--manager", "mgr")
	if err != nil {
		t.Fatalf("set --manager: %v\n%s", err, out)
	}
	last := reqs()[len(reqs())-1]
	if got := last.Body["managers"]; !reflect.DeepEqual(got, []any{"mgr", "mgr2"}) {
		t.Errorf("managers = %v, want [mgr mgr2]", got)
	}
	if !strings.Contains(out, "Managers: mgr, mgr2") {
		t.Errorf("output: %s", out)
	}

	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "set", "gpu", "--user", "alice"); err != nil {
		t.Fatal(err)
	}
	if last := reqs()[len(reqs())-1]; last.Body != nil {
		if _, has := last.Body["managers"]; has {
			t.Errorf("without --manager no managers field may be sent: %+v", last.Body)
		}
	}

	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "set", "gpu", "--no-managers"); err != nil {
		t.Fatal(err)
	}
	if m, ok := reqs()[len(reqs())-1].Body["managers"].([]any); !ok || len(m) != 0 {
		t.Errorf("--no-managers must send managers [], got %+v", reqs()[len(reqs())-1].Body)
	}

	n := len(reqs())
	for _, args := range [][]string{
		{"set", "gpu", "--manager", "mgr", "--no-managers"},
		{"set", "default", "--manager", "mgr"},
		{"set", "gpu", "--manager", "anonymous"},
	} {
		if _, err := runBlessCLI(t, "", append([]string{"--server", srv.URL, "admin", "worker-groups"}, args...)...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(reqs()) != n {
		t.Error("refused arguments must not make a request")
	}
}
