package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// workerGroupsServer fakes the worker-groups admin API and records requests.
func workerGroupsServer(t *testing.T) (*httptest.Server, func() []recordedReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedReq
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
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/admin/worker-groups":
			write(`{"default_deny":true,"groups":[
			 {"group":"default","users":[],"admins":true,"updated_by":"root","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"},
			 {"group":"gpu","users":["alice","bob"],"admins":false,"updated_by":"root","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z"}]}`)
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/v1/admin/worker-groups/"):
			users, _ := json.Marshal(body["users"])
			admins, _ := json.Marshal(body["admins"])
			managers := []byte("[]")
			if m, ok := body["managers"]; ok {
				managers, _ = json.Marshal(m)
			}
			group := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/worker-groups/")
			write(`{"group":"` + group + `","users":` + string(users) + `,"managers":` + string(managers) + `,"admins":` + string(admins) + `,"updated_by":"root"}`)
		case r.Method == "PUT" && r.URL.Path == "/api/v1/admin/worker-groups-default-deny":
			d, _ := json.Marshal(body["default_deny"])
			write(`{"default_deny":` + string(d) + `}`)
		case r.Method == "DELETE":
			write(`{"deleted":true}`)
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

func TestAdminWorkerGroups_List(t *testing.T) {
	srv, reqs := workerGroupsServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	for _, want := range []string{"default-deny: true", "GROUP", "gpu", "alice,bob", "default", "(none)", "yes", "root", "2026-01-03T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output lacks %q:\n%s", want, out)
		}
	}
	if rs := reqs(); len(rs) != 1 || rs[0].Method != "GET" || rs[0].Path != "/api/v1/admin/worker-groups" {
		t.Errorf("requests = %+v", rs)
	}

	out, err = runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(out), &body); err != nil || body["default_deny"] != true {
		t.Errorf("--json output = %s (%v)", out, err)
	}
}

func TestAdminWorkerGroups_Set(t *testing.T) {
	srv, reqs := workerGroupsServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "set", "gpu",
		"--user", "alice", "--user", "bob", "--user", "alice", "--admins")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if !strings.Contains(out, "gpu may be used by: alice, bob and server admins") {
		t.Errorf("output: %s", out)
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "PUT" || last.Path != "/api/v1/admin/worker-groups/gpu" {
		t.Fatalf("request: %+v", last)
	}
	if got := last.Body["users"]; !reflect.DeepEqual(got, []any{"alice", "bob"}) {
		t.Errorf("users = %v, want [alice bob] (duplicates removed)", got)
	}
	if last.Body["admins"] != true {
		t.Errorf("admins = %v, want true", last.Body["admins"])
	}

	// No users: an empty list is sent (never null), and the output says so.
	out, err = runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "set", "locked")
	if err != nil {
		t.Fatalf("set without users: %v\n%s", err, out)
	}
	last = reqs()[len(reqs())-1]
	if users, ok := last.Body["users"].([]any); !ok || len(users) != 0 || last.Body["admins"] != false {
		t.Errorf("body = %+v, want users [] and admins false", last.Body)
	}
	if !strings.Contains(out, "Nobody may use this group now.") {
		t.Errorf("output: %s", out)
	}
}

func TestAdminWorkerGroups_DeleteAndDefaultDeny(t *testing.T) {
	srv, reqs := workerGroupsServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "delete", "gpu")
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "DELETE" || last.Path != "/api/v1/admin/worker-groups/gpu" {
		t.Errorf("request: %+v", last)
	}

	for _, v := range []string{"true", "false"} {
		out, err = runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "default-deny", v)
		if err != nil {
			t.Fatalf("default-deny %s: %v\n%s", v, err, out)
		}
		last = reqs()[len(reqs())-1]
		if last.Method != "PUT" || last.Path != "/api/v1/admin/worker-groups-default-deny" || last.Body["default_deny"] != (v == "true") {
			t.Errorf("default-deny %s request: %+v", v, last)
		}
		if !strings.Contains(out, "default-deny set to "+v) {
			t.Errorf("output: %s", out)
		}
	}
}

// delete sends any non-empty name, so a stored rule with an invalid name can
// be removed; the name is escaped as one path segment.
func TestAdminWorkerGroups_DeleteInvalidName(t *testing.T) {
	for _, c := range []struct{ group, path string }{
		{"bad group", "/api/v1/admin/worker-groups/bad%20group"},
		{"a/b", "/api/v1/admin/worker-groups/a%2Fb"},
		{".gpu", "/api/v1/admin/worker-groups/.gpu"},
	} {
		t.Run(c.group, func(t *testing.T) {
			srv, reqs := workerGroupsServer(t)
			out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "worker-groups", "delete", c.group)
			if err != nil {
				t.Fatalf("delete %q: %v\n%s", c.group, err, out)
			}
			rs := reqs()
			if len(rs) != 1 || rs[0].Method != "DELETE" || rs[0].Path != c.path {
				t.Errorf("requests = %+v, want DELETE %s", rs, c.path)
			}
		})
	}
}

// Invalid arguments are refused before any request is made.
func TestAdminWorkerGroups_RefusedBeforeAnyRequest(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"set without group", []string{"set"}, "accepts 1 arg"},
		{"set invalid group", []string{"set", ".gpu", "--user", "alice"}, "is not a valid group name"},
		{"set empty group", []string{"set", ""}, "group name is empty"},
		{"set anonymous", []string{"set", "gpu", "--user", "anonymous"}, `users holds "anonymous"`},
		{"set empty user", []string{"set", "gpu", "--user", ""}, "empty username"},
		{"set user with whitespace", []string{"set", "gpu", "--user", "alice "}, "leading or trailing whitespace"},
		{"delete two groups", []string{"delete", "a", "b"}, "accepts 1 arg"},
		{"delete empty group", []string{"delete", ""}, "group name is empty"},
		{"set username with a control character", []string{"set", "gpu", "--user", "ali\nce"}, "contains a control character"},
		{"default-deny without value", []string{"default-deny"}, "accepts 1 arg"},
		{"default-deny not a bool", []string{"default-deny", "yes"}, `default-deny takes true or false, not "yes"`},
		{"list with an argument", []string{"list", "gpu"}, "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, reqs := workerGroupsServer(t)
			_, err := runBlessCLI(t, "", append([]string{"--server", srv.URL, "admin", "worker-groups"}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if rs := reqs(); len(rs) != 0 {
				t.Errorf("no request may be made: %+v", rs)
			}
		})
	}
}
