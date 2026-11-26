package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// groupWorkersServer fakes the worker list and the worker removal routes
// and records requests (the path as sent, escaped). Worker wrk_busy is not
// removable (409).
func groupWorkersServer(t *testing.T) (*httptest.Server, func() []recordedReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, recordedReq{r.Method, r.URL.RequestURI(), nil})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		write := func(data string) {
			_, _ = io.WriteString(w, `{"status":"ok","request_id":"r","timestamp":"2026-01-01T00:00:00Z","data":`+data+`}`)
		}
		fail := func(status int, code, msg string) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"status":"error","request_id":"r","timestamp":"2026-01-01T00:00:00Z","error":{"code":"`+code+`","message":"`+msg+`"}}`)
		}
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == "/api/v1/worker-groups/gpu/workers":
			write(`{"total":2,"workers":[
			 {"id":"wrk_1","name":"node-a","hostname":"a.example","peer_addr":"192.0.2.7","state":"offline","last_seen":"2026-01-02T03:04:05Z","registered_at":"2026-01-01T00:00:00Z","version":"abc123","runtime":"none","gpu_enabled":false,"current_task":"","key_id":"other"},
			 {"id":"wrk_2","name":"node-b","hostname":"b.example","peer_addr":"","state":"online","last_seen":"2026-01-02T03:05:00Z","registered_at":"2026-01-01T00:00:00Z","version":"","runtime":"none","gpu_enabled":false,"current_task":"","key_id":"wk_1"}]}`)
		case r.Method == "GET" && p == "/api/v1/worker-groups/empty/workers":
			write(`{"total":0,"workers":[]}`)
		case r.Method == "GET" && p == "/api/v1/worker-groups/evil/workers":
			// Worker-supplied strings with ESC, newline, tab, C1 and bidi
			// override characters.
			write(`{"total":1,"workers":[
			 {"id":"wrk_\u001b]0;x\u0007","name":"evil\u001b[31m\nname\tx","hostname":"h\r\u0085ost","peer_addr":"192.0.2.9\u001b[2J","state":"online\u001b[0m","last_seen":"2026-01-02T03:04:05Z","registered_at":"2026-01-01T00:00:00Z","version":"v` + "\xe2\x80\xae" + `1","runtime":"none","gpu_enabled":false,"current_task":"","key_id":""}]}`)
		case r.Method == "DELETE" && p == "/api/v1/worker-groups/gpu/workers/wrk_busy",
			r.Method == "DELETE" && p == "/api/v1/admin/workers/wrk_busy":
			fail(http.StatusConflict, "CONFLICT", "worker wrk_busy is online: stop it on its host")
		case r.Method == "DELETE" && strings.HasPrefix(p, "/api/v1/worker-groups/gpu/workers/"),
			r.Method == "DELETE" && strings.HasPrefix(p, "/api/v1/admin/workers/"):
			write(`{"id":"x","removed":true}`)
		default:
			fail(http.StatusNotFound, "NOT_FOUND", "no route")
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedReq(nil), reqs...)
	}
}

func TestWorkerGroups_Workers(t *testing.T) {
	srv, reqs := groupWorkersServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "workers", "gpu")
	if err != nil {
		t.Fatalf("workers: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and 2 rows:\n%s", out)
	}
	if !strings.HasPrefix(lines[0], "ID ") {
		t.Errorf("the first column must be ID: %s", lines[0])
	}
	for _, want := range []string{"NAME", "STATE", "HOSTNAME", "PEER ADDRESS", "LAST SEEN", "VERSION"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header lacks %q: %s", want, lines[0])
		}
	}
	if !strings.HasPrefix(lines[1], "wrk_1 ") || !strings.HasPrefix(lines[2], "wrk_2 ") {
		t.Errorf("rows must start with the worker id:\n%s", out)
	}
	for _, want := range []string{"node-a", "offline", "a.example", "192.0.2.7", "2026-01-02T03:04:05Z", "abc123"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row 1 lacks %q: %s", want, lines[1])
		}
	}
	if !strings.Contains(lines[2], "node-b") || !strings.Contains(lines[2], "online") || strings.Count(lines[2], " - ") < 1 {
		t.Errorf("row 2 (empty peer address and version as -): %s", lines[2])
	}
	if rs := reqs(); len(rs) != 1 || rs[0].Method != "GET" || rs[0].Path != "/api/v1/worker-groups/gpu/workers" {
		t.Errorf("requests = %+v", rs)
	}

	out, err = runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "workers", "gpu", "--json")
	if err != nil || !strings.Contains(out, `"id": "wrk_1"`) || !strings.Contains(out, `"key_id": "other"`) {
		t.Errorf("workers --json: %v\n%s", err, out)
	}

	out, err = runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "workers", "empty")
	if err != nil || !strings.Contains(out, "No workers in group empty.") {
		t.Errorf("workers of an empty group: %v\n%s", err, out)
	}
}

// Worker-supplied strings never put a control character on the terminal:
// the table keeps one line per worker and no ESC, CR, BEL, C1 or bidi
// override reaches the output; --json keeps them escaped by encoding/json.
func TestWorkerGroups_WorkersControlCharacters(t *testing.T) {
	srv, _ := groupWorkersServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "workers", "evil")
	if err != nil {
		t.Fatalf("workers: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and 1 row (no newline from a worker string):\n%q", out)
	}
	for _, bad := range []string{"\x1b", "\r", "\a", "\u0085", "\u202e"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q:\n%q", bad, out)
		}
	}
	for _, want := range []string{"wrk_?]0;x?", "evil?[31m?name?x", "h??ost", "192.0.2.9?[2J", "online?[0m", "v?1"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row lacks %q: %q", want, lines[1])
		}
	}

	out, err = runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "workers", "evil", "--json")
	if err != nil {
		t.Fatalf("workers --json: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b") || !strings.Contains(out, `evil\u001b[31m\nname\tx`) {
		t.Errorf("workers --json must keep the strings escaped:\n%q", out)
	}
}

func TestTerminalSafe(t *testing.T) {
	tests := []struct{ in, want string }{
		{"node-a.example", "node-a.example"},
		{"", ""},
		{"a\tb\nc\x1bd", "a?b?c?d"},
		{"\x7f\u0085\u202e\u2028", "????"},
		{"nœud-é", "nœud-é"},
	}
	for _, tt := range tests {
		if got := terminalSafe(tt.in); got != tt.want {
			t.Errorf("terminalSafe(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWorkerGroups_RemoveWorker(t *testing.T) {
	srv, reqs := groupWorkersServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "remove-worker", "gpu", "wrk_1")
	if err != nil || !strings.Contains(out, "Removed worker wrk_1 of group gpu") {
		t.Fatalf("remove-worker: %v\n%s", err, out)
	}
	if last := reqs()[len(reqs())-1]; last.Method != "DELETE" || last.Path != "/api/v1/worker-groups/gpu/workers/wrk_1" {
		t.Errorf("remove-worker request: %+v", last)
	}

	// The server's refusal reaches the user.
	_, err = runBlessCLI(t, "", "--server", srv.URL, "worker-groups", "remove-worker", "gpu", "wrk_busy")
	if err == nil || !strings.Contains(err.Error(), "stop it on its host") {
		t.Errorf("remove-worker of an online worker: %v, want the server's 409 message", err)
	}

	// Invalid arguments make no request.
	n := len(reqs())
	for _, args := range [][]string{
		{"remove-worker", ".gpu", "wrk_1"},
		{"remove-worker", "gpu", ""},
		{"remove-worker", "gpu"},
		{"workers", ""},
	} {
		if _, err := runBlessCLI(t, "", append([]string{"--server", srv.URL, "worker-groups"}, args...)...); err == nil {
			t.Errorf("%v: want an error", args)
		}
	}
	if len(reqs()) != n {
		t.Errorf("refused arguments made requests: %+v", reqs()[n:])
	}
}

func TestAdminRemoveWorker(t *testing.T) {
	srv, reqs := groupWorkersServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "remove-worker", "wrk_9")
	if err != nil || !strings.Contains(out, "Removed worker wrk_9") {
		t.Fatalf("admin remove-worker: %v\n%s", err, out)
	}
	if last := reqs()[len(reqs())-1]; last.Method != "DELETE" || last.Path != "/api/v1/admin/workers/wrk_9" {
		t.Errorf("admin remove-worker request: %+v", last)
	}
	_, err = runBlessCLI(t, "", "--server", srv.URL, "admin", "remove-worker", "wrk_busy")
	if err == nil || !strings.Contains(err.Error(), "stop it on its host") {
		t.Errorf("admin remove-worker of an online worker: %v", err)
	}
	n := len(reqs())
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "remove-worker", ""); err == nil {
		t.Error("an empty worker id must be refused")
	}
	if len(reqs()) != n {
		t.Error("an empty worker id must make no request")
	}
}
