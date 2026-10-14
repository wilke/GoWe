package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type recordedReq struct {
	Method, Path string
	Body         map[string]any
}

// blessingServer fakes the tool-blessings admin API and records requests.
func blessingServer(t *testing.T) (*httptest.Server, func() []recordedReq) {
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
		case r.Method == "POST" && r.URL.Path == "/api/v1/admin/tool-blessings/inspect":
			write(`{"tool_id":"helper","tool_hash":"sha256:abc","image_ref":"python.sif","requests_token":true,
			"workflow_id":"wf_1","workflow_name":"bvbrc-helper","warnings":[],
			"summary":{"class":"CommandLineTool","base_command":["python3","run.py"],
			  "scripts":[{"source":"InitialWorkDirRequirement:run.py","content":"import os\nprint(1)\n"}],
			  "flags":["declared-stdout: tool declares stdout: out.txt","verbose-debug: --verbose"]}}`)
		case r.Method == "POST" && r.URL.Path == "/api/v1/admin/tool-blessings":
			write(`{"id":"tb_1","tool_hash":"sha256:abc","image_sha256s":[],"env_names":[],"worker_groups":[]}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/admin/tool-blessings/pending":
			write(`{"total":1,"pending":[{"tool_id":"helper","tool_hash":"sha256:abc","image_ref":"python.sif","workflow_name":"bvbrc-helper"}]}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/admin/tool-blessings":
			write(`{"total":2,"blessings":[
			 {"id":"tb_1","tool_name":"bvbrc-helper#helper","tool_hash":"sha256:abc","image_sha256s":["aa"],"worker_groups":[],"blessed_by":"admin"},
			 {"id":"tb_2","tool_name":"old","tool_hash":"sha256:def","image_sha256s":["bb"],"worker_groups":["g"],"blessed_by":"admin","revoked_at":"2026-01-01T00:00:00Z","revoke_reason":"found echo"}]}`)
		default:
			write(`{}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedReq(nil), reqs...)
	}
}

func runBlessCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func writeSIF(t *testing.T, content string) (path, sum string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "tool.sif")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(content))
	return path, hex.EncodeToString(h[:])
}

func TestAdminBless_RequiresImageBeforeAnyRequest(t *testing.T) {
	srv, reqs := blessingServer(t)
	_, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "bless", "bvbrc-helper")
	if err == nil || !strings.Contains(err.Error(), "pins its image") {
		t.Fatalf("err = %v", err)
	}
	if len(reqs()) != 0 {
		t.Errorf("no request may be made without an image: %v", reqs())
	}
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "bless", "x", "--image-sha256", "nothex"); err == nil {
		t.Error("invalid digest must be refused")
	}
}

func TestAdminBless_ReportAndConfirm(t *testing.T) {
	srv, reqs := blessingServer(t)
	sif, sum := writeSIF(t, "pretend image bytes")

	// Declining aborts without creating anything.
	out, err := runBlessCLI(t, "no\n", "--server", srv.URL, "admin", "bless", "bvbrc-helper", "--image-file", sif)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("declined: err=%v", err)
	}
	for _, want := range []string{
		"sha256:abc", "python.sif", "----- script: InitialWorkDirRequirement:run.py -----", "import os\nprint(1)\n",
		"REVIEW FLAGS (2)", "declared-stdout", "verbose-debug", sum,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	for _, r := range reqs() {
		if r.Method == "POST" && r.Path == "/api/v1/admin/tool-blessings" {
			t.Fatal("declined confirmation must not create a blessing")
		}
	}

	// Confirming creates it with the locally computed digest and the flags.
	out, err = runBlessCLI(t, "yes\n", "--server", srv.URL, "admin", "bless", "bvbrc-helper",
		"--image-file", sif, "--image-sha256", "sha256:"+strings.Repeat("ab", 32),
		"--env", "BVBRC_TOKEN", "--groups", "default,anlseq", "--note", "reviewed", "--tool", "helper")
	if err != nil {
		t.Fatalf("bless: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Blessed sha256:abc as tb_1") {
		t.Errorf("output: %s", out)
	}
	var create recordedReq
	for _, r := range reqs() {
		if r.Method == "POST" && r.Path == "/api/v1/admin/tool-blessings" {
			create = r
		}
	}
	if create.Body == nil {
		t.Fatal("no create request")
	}
	shas, _ := create.Body["image_sha256s"].([]any)
	if len(shas) != 2 || shas[0] != sum || shas[1] != strings.Repeat("ab", 32) {
		t.Errorf("image_sha256s = %v", shas)
	}
	if create.Body["tool_hash"] != "sha256:abc" || create.Body["workflow"] != "bvbrc-helper" || create.Body["tool_id"] != "helper" ||
		create.Body["note"] != "reviewed" {
		t.Errorf("create body = %v", create.Body)
	}
	if g, _ := create.Body["worker_groups"].([]any); len(g) != 2 {
		t.Errorf("groups = %v", create.Body["worker_groups"])
	}
}

func TestAdminBless_YesSkipsPrompt_FileIsInspectedByContent(t *testing.T) {
	srv, reqs := blessingServer(t)
	cwlFile := filepath.Join(t.TempDir(), "tool.cwl")
	if err := os.WriteFile(cwlFile, []byte("class: CommandLineTool\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, sum := writeSIF(t, "x")
	_, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "bless", cwlFile, "--image-sha256", sum, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	rs := reqs()
	if rs[0].Path != "/api/v1/admin/tool-blessings/inspect" || rs[0].Body["cwl"] != "class: CommandLineTool\n" {
		t.Errorf("file must be sent as cwl: %+v", rs[0])
	}
	if _, has := rs[1].Body["workflow"]; has {
		t.Errorf("file blessing must go by hash, not workflow: %+v", rs[1].Body)
	}
}

func TestAdminBlessings_ListPendingAndAll(t *testing.T) {
	srv, reqs := blessingServer(t)
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "blessings", "--pending")
	if err != nil || !strings.Contains(out, "bvbrc-helper") || !strings.Contains(out, "sha256:abc") {
		t.Fatalf("pending: %v\n%s", err, out)
	}
	out, err = runBlessCLI(t, "", "--server", srv.URL, "admin", "blessings", "--all")
	if err != nil || !strings.Contains(out, "revoked: found echo") || !strings.Contains(out, "active") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var sawAll bool
	for _, r := range reqs() {
		if strings.Contains(r.Path, "include_revoked=true") {
			sawAll = true
		}
	}
	if !sawAll {
		t.Error("--all must request include_revoked=true")
	}
}

func TestAdminBlessings_AddImage(t *testing.T) {
	srv, reqs := blessingServer(t)
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "blessings", "add-image", "tb_1"); err == nil {
		t.Error("add-image without a digest must fail")
	}
	sif, sum := writeSIF(t, "second build")
	out, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "blessings", "add-image", "tb_1", "--image-file", sif)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "POST" || last.Path != "/api/v1/admin/tool-blessings/tb_1/images" || last.Body["image_sha256"] != sum {
		t.Errorf("request: %+v", last)
	}
}

func TestAdminUnbless(t *testing.T) {
	srv, reqs := blessingServer(t)
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "unbless", "tb_1"); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("reason required: %v", err)
	}
	if len(reqs()) != 0 {
		t.Error("no request without a reason")
	}
	if _, err := runBlessCLI(t, "", "--server", srv.URL, "admin", "unbless", "tb_1", "--reason", "found echo"); err != nil {
		t.Fatal(err)
	}
	last := reqs()[len(reqs())-1]
	if last.Method != "DELETE" || last.Path != "/api/v1/admin/tool-blessings/tb_1" || last.Body["reason"] != "found echo" {
		t.Errorf("request: %+v", last)
	}
}
