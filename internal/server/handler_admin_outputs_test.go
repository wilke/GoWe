package server

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// Tokens in the BV-BRC pipe-delimited format; expiry is 2100-01-01.
const (
	adminAuthToken = "un=admin|tokenid=t-admin|expiry=4102444800|sig=x"
	userAuthToken  = "un=tester|tokenid=t-user|expiry=4102444800|sig=y"
	ownerToken     = "un=tester@bvbrc|tokenid=t-owner|expiry=4102444800|sig=z"
	wsHome         = "/tester@bvbrc/home/results"
)

// adminOutputsServer builds a server wired to the fake Workspace, with
// "admin" granted the admin role through the CLI-admins source and the given
// directories allowed as re-delivery sources.
func adminOutputsServer(t *testing.T, withStager bool, sourceDirs ...string) (*Server, *bvbrctest.Server) {
	t.Helper()
	f := bvbrctest.New(t)
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	opts := []Option{WithAdminConfig(adminCfg), WithRedeliverSourceDirs(sourceDirs)}
	if withStager {
		stager := staging.NewWorkspaceStager(staging.WorkspaceConfig{
			WorkspaceURL: f.WorkspaceURL(),
			MaxRetries:   1,
		}, nil)
		opts = append(opts, WithWorkspaceStager(stager))
	}
	return testServer(opts...), f
}

func postAs(t *testing.T, srv *Server, path, token string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest("POST", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func decodeReport(t *testing.T, env envelope) outputReport {
	t.Helper()
	var rep outputReport
	if err := json.Unmarshal(env.Data, &rep); err != nil {
		t.Fatalf("decode report: %v (data=%s)", err, string(env.Data))
	}
	return rep
}

func sha1Sum(b []byte) string {
	h := sha1.Sum(b)
	return "sha1$" + hex.EncodeToString(h[:])
}

// binaryPayload is a payload with every byte value, which the pre-#174 inline
// JSON path corrupts.
func binaryPayload(reps int) []byte {
	out := make([]byte, 0, 256*reps)
	for r := 0; r < reps; r++ {
		for i := 0; i < 256; i++ {
			out = append(out, byte(i))
		}
	}
	return out
}

// corruptedOf mimics the damage: every non-ASCII byte replaced by U+FFFD.
func corruptedOf(good []byte) []byte {
	var out []byte
	for _, b := range good {
		if b < 0x80 {
			out = append(out, b)
		} else {
			out = append(out, []byte("�")...)
		}
	}
	return out
}

// uploadToFake stores content at wsPath through the real protocol so the fake
// mints a Shock node the download path can serve.
func uploadToFake(t *testing.T, f *bvbrctest.Server, wsPath string, content []byte) {
	t.Helper()
	c := bvbrc.NewClient(bvbrc.Config{WorkspaceURL: f.WorkspaceURL(), Token: ownerToken}, nil)
	if _, err := c.WorkspaceUploadFile(context.Background(), wsPath, content, bvbrc.WorkspaceTypeUnspecified); err != nil {
		t.Fatalf("seed upload %s: %v", wsPath, err)
	}
}

// fileObj builds a CWL File output object the way the worker records it.
func fileObj(location, basename string, content []byte) map[string]any {
	return map[string]any{
		"class":    "File",
		"location": location,
		"basename": basename,
		"checksum": sha1Sum(content),
		"size":     len(content),
	}
}

// writeOriginal writes the staged original as <stageDir>/<taskID>/<name> and
// returns its file:// location.
func writeOriginal(t *testing.T, stageDir, taskID, name string, content []byte) string {
	t.Helper()
	dir := filepath.Join(stageDir, taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return "file://" + p
}

// seedSubmission stores a terminal submission owned by the fake's user. An
// upload_failed one is FAILED with the scheduler's error code, as in prod.
func seedSubmission(t *testing.T, srv *Server, wfID, id string, outputs map[string]any, outputState string) *model.Submission {
	t.Helper()
	sub := &model.Submission{
		ID:                id,
		WorkflowID:        wfID,
		WorkflowName:      "test-workflow",
		State:             model.SubmissionStateCompleted,
		Inputs:            map[string]any{},
		Outputs:           outputs,
		SubmittedBy:       "tester@bvbrc",
		OutputDestination: "ws://" + wsHome,
		OutputState:       outputState,
		UserToken:         ownerToken,
		TokenExpiry:       time.Now().Add(24 * time.Hour),
		CreatedAt:         time.Now().UTC(),
	}
	if outputState == outputStateUploadFailed {
		sub.State = model.SubmissionStateFailed
		sub.Error = &model.SubmissionError{Code: outputStagingFailedCode, Message: "workspace output upload failed"}
	}
	if err := srv.store.CreateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("seed submission %s: %v", id, err)
	}
	// The INSERT does not carry the error column (the scheduler sets it on
	// update); persist it the same way so the row looks like prod's.
	if sub.Error != nil {
		if err := srv.store.UpdateSubmission(context.Background(), sub); err != nil {
			t.Fatalf("seed submission error %s: %v", id, err)
		}
	}
	return sub
}

func seedTask(t *testing.T, srv *Server, subID, taskID string, execType model.ExecutorType, outputs map[string]any) {
	t.Helper()
	task := &model.Task{
		ID:           taskID,
		SubmissionID: subID,
		StepID:       "work",
		State:        model.TaskStateSuccess,
		ExecutorType: execType,
		Inputs:       map[string]any{},
		Outputs:      outputs,
		Job:          map[string]any{},
		ScatterIndex: -1,
	}
	if err := srv.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed task %s: %v", taskID, err)
	}
}

// deliveredFixture is a submission whose single ws:// output holds corrupted
// bytes while the recorded checksum/size describe the good ones, and a task
// row pointing at the intact original on the stage-out dir.
type deliveredFixture struct {
	sub      *model.Submission
	wsPath   string
	good     []byte
	original string // local path
}

func seedDelivered(t *testing.T, srv *Server, f *bvbrctest.Server, stage, outputState string) deliveredFixture {
	t.Helper()
	wfID := createTestWorkflow(t, srv)
	good := binaryPayload(8)
	wsPath := wsHome + "/chunks.bin"
	uploadToFake(t, f, wsPath, corruptedOf(good))

	loc := writeOriginal(t, stage, "task_a", "chunks.bin", good)
	sub := seedSubmission(t, srv, wfID, "sub_delivered_"+outputState, map[string]any{
		"result": fileObj("ws://"+wsPath, "chunks.bin", good),
	}, outputState)
	seedTask(t, srv, sub.ID, "task_a", model.ExecutorTypeWorker, map[string]any{
		"result": fileObj(loc, "chunks.bin", good),
	})
	return deliveredFixture{sub: sub, wsPath: wsPath, good: good, original: loc[len("file://"):]}
}

// manifestCreates returns the Workspace.create calls that targeted the
// output manifest.
func manifestCreates(t *testing.T, f *bvbrctest.Server) []string {
	t.Helper()
	var out []string
	for _, c := range f.CallsTo("Workspace.create") {
		var p struct {
			Objects [][]any `json:"objects"`
		}
		if err := json.Unmarshal(c.Params, &p); err != nil {
			t.Fatal(err)
		}
		for _, spec := range p.Objects {
			if path, _ := spec[0].(string); strings.HasSuffix(path, "/"+staging.OutputManifestName) {
				out = append(out, path)
			}
		}
	}
	return out
}

func TestAdminVerifyOutputs_ReportsMismatch(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "delivered")

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/verify-outputs", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rep := decodeReport(t, env)

	if rep.Summary.Total != 1 || rep.Summary.Mismatched != 1 || rep.Summary.OK != 0 {
		t.Fatalf("summary = %+v, want 1 mismatched", rep.Summary)
	}
	got := rep.Files[0]
	if got.OK || got.Error != "checksum mismatch" {
		t.Errorf("file = %+v, want checksum mismatch", got)
	}
	if got.ExpectedChecksum != sha1Sum(fx.good) || got.ActualChecksum == "" || got.ActualChecksum == got.ExpectedChecksum {
		t.Errorf("checksums: expected=%s actual=%s", got.ExpectedChecksum, got.ActualChecksum)
	}
	if got.ExpectedSize != int64(len(fx.good)) || got.ActualSize != int64(len(corruptedOf(fx.good))) {
		t.Errorf("sizes: expected=%d actual=%d", got.ExpectedSize, got.ActualSize)
	}
	if got.Location != "ws://"+fx.wsPath || got.SubmissionID != fx.sub.ID {
		t.Errorf("location/submission = %s/%s", got.Location, got.SubmissionID)
	}

	// Verify is read-only: nothing in the store or the workspace changed.
	after, _ := srv.store.GetSubmission(context.Background(), fx.sub.ID)
	if after.OutputState != "delivered" {
		t.Errorf("output_state changed to %q", after.OutputState)
	}
	if bytes.Equal(f.Bytes(fx.wsPath), fx.good) {
		t.Errorf("verify must not upload")
	}
}

func TestAdminRedeliver_ReuploadsAndVerifies(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "delivered")

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rep := decodeReport(t, env)

	if rep.Summary.Total != 1 || rep.Summary.OK != 1 || rep.Summary.Reuploaded != 1 {
		t.Fatalf("summary = %+v, want 1 reuploaded+ok", rep.Summary)
	}
	got := rep.Files[0]
	if !got.OK || got.Action != outputActionReuploaded || got.Source != fx.original {
		t.Errorf("file = %+v", got)
	}
	if got.ActualChecksum != sha1Sum(fx.good) || got.ActualSize != int64(len(fx.good)) {
		t.Errorf("re-verify recorded %s/%d", got.ActualChecksum, got.ActualSize)
	}
	if !bytes.Equal(f.Bytes(fx.wsPath), fx.good) {
		t.Errorf("workspace does not hold the good bytes after redeliver")
	}
	if rep.OutputState != "delivered" || rep.StateRestored {
		t.Errorf("report state = %s/%s restored=%v", rep.State, rep.OutputState, rep.StateRestored)
	}
	// Same path and already delivered: no row write, no manifest rewrite.
	if rep.Updated || rep.ManifestUploaded {
		t.Errorf("updated=%v manifest=%v, but location and output_state were unchanged", rep.Updated, rep.ManifestUploaded)
	}
	if m := manifestCreates(t, f); len(m) != 0 {
		t.Errorf("manifest written without a submission change: %v", m)
	}

	// A second verify is clean.
	_, env = postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/verify-outputs", adminAuthToken)
	if rep := decodeReport(t, env); rep.Summary.OK != 1 {
		t.Errorf("post-redeliver verify summary = %+v", rep.Summary)
	}
}

func TestAdminRedeliver_DryRunChangesNothing(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "upload_failed")
	putsBefore := len(f.Puts())

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver?dry_run=true", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rep := decodeReport(t, env)
	if !rep.DryRun || rep.Summary.WouldReupload != 1 || rep.Summary.Reuploaded != 0 || rep.Updated {
		t.Fatalf("report = %+v", rep)
	}
	if got := rep.Files[0]; got.Action != outputActionWouldReupload || got.Source != fx.original || got.OK {
		t.Errorf("file = %+v", got)
	}
	if len(f.Puts()) != putsBefore {
		t.Errorf("dry run performed %d uploads", len(f.Puts())-putsBefore)
	}
	if bytes.Equal(f.Bytes(fx.wsPath), fx.good) {
		t.Errorf("dry run changed workspace content")
	}
	after, _ := srv.store.GetSubmission(context.Background(), fx.sub.ID)
	if after.OutputState != "upload_failed" || after.State != model.SubmissionStateFailed {
		t.Errorf("dry run changed submission: state=%s output_state=%q", after.State, after.OutputState)
	}
}

func TestAdminRedeliver_OriginalMissing(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "upload_failed")
	if err := os.Remove(fx.original); err != nil {
		t.Fatal(err)
	}

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rep := decodeReport(t, env)
	if rep.Summary.OriginalMissing != 1 || rep.Summary.OK != 0 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	if got := rep.Files[0]; got.Action != outputActionOriginalMissing || got.OK || got.Error == "" {
		t.Errorf("file = %+v", got)
	}
	// Not every file verified, so the state must not flip.
	after, _ := srv.store.GetSubmission(context.Background(), fx.sub.ID)
	if after.OutputState != "upload_failed" || after.State != model.SubmissionStateFailed {
		t.Errorf("submission changed: state=%s output_state=%q", after.State, after.OutputState)
	}
}

// M1: candidates are admitted by stat only — a FIFO (which would block an
// open forever) and a regular file of the wrong size are skipped without
// being opened, and the handler returns promptly.
func TestAdminRedeliver_SkipsFifoAndWrongSizeCandidates(t *testing.T) {
	stage := t.TempDir()
	srv, _ := adminOutputsServer(t, true, stage)
	wfID := createTestWorkflow(t, srv)
	good := binaryPayload(4)

	// The output's own file:// location is a FIFO: the self-candidate.
	fifoDir := filepath.Join(stage, "task_fifo")
	if err := os.MkdirAll(fifoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(fifoDir, "out.bin")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	// The index candidate: right checksum key, wrong on-disk size.
	wrongSize := writeOriginal(t, stage, "task_wrong", "out.bin", append(good, 'x'))

	sub := seedSubmission(t, srv, wfID, "sub_fifo", map[string]any{
		"result": fileObj("file://"+fifo, "out.bin", good),
	}, "upload_failed")
	seedTask(t, srv, sub.ID, "task_wrong", model.ExecutorTypeWorker, map[string]any{
		"result": fileObj(wrongSize, "out.bin", good), // lies about size: says len(good)
	})

	done := make(chan outputReport, 1)
	go func() {
		w, env := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/redeliver", adminAuthToken)
		if w.Code != http.StatusOK {
			t.Errorf("status=%d body=%s", w.Code, w.Body.String())
		}
		done <- decodeReport(t, env)
	}()
	var rep outputReport
	select {
	case rep = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("redeliver hung: a candidate was opened instead of being skipped by stat")
	}

	if rep.Summary.OriginalMissing != 1 {
		t.Fatalf("summary = %+v files=%+v", rep.Summary, rep.Files)
	}
	got := rep.Files[0]
	if got.Action != outputActionOriginalMissing {
		t.Errorf("action = %s", got.Action)
	}
	if !strings.Contains(got.Error, "not a regular file") {
		t.Errorf("FIFO not reported as skipped: %s", got.Error)
	}
	if !strings.Contains(got.Error, "has size") {
		t.Errorf("wrong-size candidate not reported as skipped: %s", got.Error)
	}
}

// M2(b): the persist step is compare-and-set against the state loaded at the
// start of the request; a concurrent change → 409 and nothing written.
func TestAdminRedeliver_ConcurrentChangeIs409(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "upload_failed")

	// Between the re-upload and the write (the second get_download_url is
	// the post-upload re-verify), another writer flips output_state.
	var calls atomic.Int32
	f.Intercept = func(method string, _ json.RawMessage) (int, string) {
		if method != "Workspace.get_download_url" || calls.Add(1) != 2 {
			return 0, ""
		}
		cur, err := srv.store.GetSubmission(context.Background(), fx.sub.ID)
		if err != nil {
			t.Errorf("load for concurrent write: %v", err)
			return 0, ""
		}
		cur.OutputState = "delivered"
		if err := srv.store.UpdateSubmission(context.Background(), cur); err != nil {
			t.Errorf("concurrent write: %v", err)
		}
		return 0, ""
	}

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409; body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "changed concurrently") {
		t.Errorf("error = %+v", env.Error)
	}
	// The concurrent writer's row survives untouched: state FAILED and the
	// error record were not restored by the losing request.
	after, _ := srv.store.GetSubmission(context.Background(), fx.sub.ID)
	if after.OutputState != "delivered" || after.State != model.SubmissionStateFailed || after.Error == nil {
		t.Errorf("row overwritten: state=%s output_state=%q error=%v", after.State, after.OutputState, after.Error)
	}
}

// S1: originals are only read from under --redeliver-source-dirs.
func TestAdminRedeliver_SourceDirAllowlist(t *testing.T) {
	t.Run("outside allowlist is denied", func(t *testing.T) {
		stage := t.TempDir()
		srv, f := adminOutputsServer(t, true, t.TempDir()) // a different dir
		fx := seedDelivered(t, srv, f, stage, "upload_failed")

		_, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
		rep := decodeReport(t, env)
		if got := rep.Files[0]; got.Action != outputActionOriginalMissing || !strings.Contains(got.Error, "outside --redeliver-source-dirs") {
			t.Errorf("file = %+v", got)
		}
		if bytes.Equal(f.Bytes(fx.wsPath), fx.good) {
			t.Errorf("uploaded from a denied path")
		}
	})

	t.Run("no allowlist refuses local recovery", func(t *testing.T) {
		stage := t.TempDir()
		srv, f := adminOutputsServer(t, true) // none configured
		fx := seedDelivered(t, srv, f, stage, "upload_failed")

		_, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
		rep := decodeReport(t, env)
		if got := rep.Files[0]; got.Action != outputActionOriginalMissing || !strings.Contains(got.Error, "no --redeliver-source-dirs") {
			t.Errorf("file = %+v", got)
		}
		after, _ := srv.store.GetSubmission(context.Background(), fx.sub.ID)
		if after.OutputState != "upload_failed" {
			t.Errorf("output_state = %q", after.OutputState)
		}
	})

	t.Run("symlink escaping the allowlist is denied", func(t *testing.T) {
		stage := t.TempDir()
		outside := t.TempDir()
		srv, f := adminOutputsServer(t, true, stage)
		wfID := createTestWorkflow(t, srv)
		good := binaryPayload(4)
		wsPath := wsHome + "/esc.bin"
		uploadToFake(t, f, wsPath, corruptedOf(good))

		real := writeOriginal(t, outside, "task_x", "esc.bin", good)
		linkDir := filepath.Join(stage, "task_x")
		if err := os.MkdirAll(linkDir, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "esc.bin")
		if err := os.Symlink(real[len("file://"):], link); err != nil {
			t.Fatal(err)
		}
		sub := seedSubmission(t, srv, wfID, "sub_esc", map[string]any{
			"result": fileObj("ws://"+wsPath, "esc.bin", good),
		}, "delivered")
		seedTask(t, srv, sub.ID, "task_x", model.ExecutorTypeWorker, map[string]any{
			"result": fileObj("file://"+link, "esc.bin", good),
		})

		_, env := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/redeliver", adminAuthToken)
		rep := decodeReport(t, env)
		if got := rep.Files[0]; got.Action != outputActionOriginalMissing {
			t.Errorf("symlink escape was followed: %+v", got)
		}
		if bytes.Equal(f.Bytes(wsPath), good) {
			t.Errorf("uploaded through an escaping symlink")
		}
	})
}

// An upload_failed submission still carries file:// outputs (never uploaded)
// and was FAILED by the scheduler; redeliver uploads them under the
// destination, rewrites the locations, restores COMPLETED, and rewrites the
// output manifest (S2).
func TestAdminRedeliver_UploadFailedRestoresState(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	wfID := createTestWorkflow(t, srv)
	good := binaryPayload(4)
	loc := writeOriginal(t, stage, "task_b", "model.pdb", good)

	sub := seedSubmission(t, srv, wfID, "sub_failed", map[string]any{
		"structures": map[string]any{
			"class":    "Directory",
			"basename": "models",
			"listing":  []any{fileObj(loc, "model.pdb", good)},
		},
	}, "upload_failed")
	seedTask(t, srv, sub.ID, "task_b", model.ExecutorTypeWorker, map[string]any{
		"structures": map[string]any{"class": "Directory", "basename": "models", "listing": []any{fileObj(loc, "model.pdb", good)}},
	})

	// Verify first: the file:// output is reported as not delivered.
	_, env := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/verify-outputs", adminAuthToken)
	if rep := decodeReport(t, env); rep.Summary.Errors != 1 || rep.Files[0].Error == "" {
		t.Fatalf("verify report = %+v", rep)
	}

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/redeliver", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rep := decodeReport(t, env)
	wantPath := wsHome + "/models/model.pdb"
	if rep.Summary.Reuploaded != 1 || rep.Summary.OK != 1 || !rep.Updated || !rep.StateRestored {
		t.Fatalf("report = %+v", rep)
	}
	if got := rep.Files[0]; got.Location != "ws://"+wantPath || got.Action != outputActionReuploaded {
		t.Errorf("file = %+v", got)
	}
	if !bytes.Equal(f.Bytes(wantPath), good) {
		t.Errorf("workspace bytes at %s do not match", wantPath)
	}

	after, _ := srv.store.GetSubmission(context.Background(), sub.ID)
	if after.State != model.SubmissionStateCompleted || after.OutputState != "delivered" || after.Error != nil {
		t.Errorf("after = state=%s output_state=%s error=%v", after.State, after.OutputState, after.Error)
	}
	listing := after.Outputs["structures"].(map[string]any)["listing"].([]any)
	if loc := listing[0].(map[string]any)["location"]; loc != "ws://"+wantPath {
		t.Errorf("location not rewritten: %v", loc)
	}

	// S2: the manifest was rewritten with the ws:// tree.
	if !rep.ManifestUploaded || rep.ManifestError != "" {
		t.Errorf("manifest_uploaded=%v error=%q", rep.ManifestUploaded, rep.ManifestError)
	}
	manifestPath := wsHome + "/" + staging.OutputManifestName
	if m := manifestCreates(t, f); len(m) != 1 || m[0] != manifestPath {
		t.Errorf("manifest creates = %v, want [%s]", m, manifestPath)
	}
	var manifest struct {
		SubmissionID string         `json:"submission_id"`
		State        string         `json:"state"`
		Outputs      map[string]any `json:"outputs"`
	}
	if err := json.Unmarshal(f.Bytes(manifestPath), &manifest); err != nil {
		t.Fatalf("manifest not readable: %v", err)
	}
	if manifest.SubmissionID != sub.ID || manifest.State != "COMPLETED" || !strings.Contains(string(f.Bytes(manifestPath)), "ws://"+wantPath) {
		t.Errorf("manifest content = %+v", manifest)
	}
}

// S3: one re-delivery per submission at a time.
func TestAdminRedeliver_InFlightLock(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "upload_failed")

	parked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.HoldShockBody = func(string) <-chan struct{} {
		var ch <-chan struct{}
		once.Do(func() {
			close(parked)
			ch = release
		})
		return ch
	}

	type result struct {
		code int
		rep  outputReport
	}
	first := make(chan result, 1)
	go func() {
		w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
		var rep outputReport
		_ = json.Unmarshal(env.Data, &rep)
		first <- result{w.Code, rep}
	}()

	select {
	case <-parked:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("first redeliver never reached the Shock PUT")
	}

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
	if w.Code != http.StatusConflict || env.Error == nil || !strings.Contains(env.Error.Message, "re-delivery in progress") {
		t.Errorf("second call: status=%d error=%+v, want 409 in progress", w.Code, env.Error)
	}
	// The lock is in-memory only: the row never says "uploading".
	if cur, _ := srv.store.GetSubmission(context.Background(), fx.sub.ID); cur.OutputState != "upload_failed" {
		t.Errorf("output_state persisted as %q during re-delivery", cur.OutputState)
	}

	close(release)
	r := <-first
	if r.code != http.StatusOK || r.rep.Summary.Failed != 1 {
		t.Errorf("first call: status=%d summary=%+v (abandoned upload must fail the file)", r.code, r.rep.Summary)
	}

	// Lock released: a third call is admitted (and fails the same way, since
	// MaxRetries is 1 and the fake now serves the PUT normally).
	if w, _ := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken); w.Code == http.StatusConflict {
		t.Errorf("lock not released after the first request finished")
	}
}

// S5: a workspace that serves different bytes after a successful PUT fails
// the file at re-verification and leaves the submission untouched.
func TestAdminRedeliver_ReverifyMismatchFails(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "upload_failed")
	oldNode := f.Object(fx.wsPath).NodeID

	// On the post-upload re-verify, point the object back at the corrupted
	// node (overwritten nodes are never deleted, as in Shock).
	var calls atomic.Int32
	f.Intercept = func(method string, _ json.RawMessage) (int, string) {
		if method == "Workspace.get_download_url" && calls.Add(1) == 2 {
			o := f.Object(fx.wsPath)
			o.NodeID = oldNode
			f.Put(*o)
		}
		return 0, ""
	}

	w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", adminAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	rep := decodeReport(t, env)
	got := rep.Files[0]
	if got.Action != outputActionFailed || got.OK || !strings.HasPrefix(got.Error, "re-verify after upload") {
		t.Errorf("file = %+v", got)
	}
	if rep.Summary.Failed != 1 || rep.Summary.Reuploaded != 0 || rep.Updated {
		t.Errorf("report = %+v", rep)
	}
	after, _ := srv.store.GetSubmission(context.Background(), fx.sub.ID)
	if after.OutputState != "upload_failed" || after.State != model.SubmissionStateFailed {
		t.Errorf("submission changed: state=%s output_state=%q", after.State, after.OutputState)
	}
}

// Child submissions are included: their ws:// outputs are verified and their
// task rows are searched for originals (checksum+size, not basename).
func TestAdminOutputs_ChildSubmissionsIncluded(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	wfID := createTestWorkflow(t, srv)

	good := binaryPayload(6)
	parentWS := wsHome + "/shard.bin"
	uploadToFake(t, f, parentWS, corruptedOf(good))

	childGood := []byte("child text output\n")
	childWS := wsHome + "/child/child.txt"
	uploadToFake(t, f, childWS, childGood)

	parent := seedSubmission(t, srv, wfID, "sub_parent", map[string]any{
		"gathered": []any{fileObj("ws://"+parentWS, "shard.bin", good)},
	}, "delivered")
	// Proxy task: mirrors the child's outputs, as the scheduler does, but a
	// decoy with the same basename and a different checksum must not match.
	decoy := writeOriginal(t, stage, "task_decoy", "shard.bin", []byte("not the same bytes"))
	seedTask(t, srv, parent.ID, "task_proxy", model.ExecutorTypeSubworkflow, map[string]any{})
	seedTask(t, srv, parent.ID, "task_decoy", model.ExecutorTypeWorker, map[string]any{
		"x": fileObj(decoy, "shard.bin", []byte("not the same bytes")),
	})

	child := &model.Submission{
		ID:           "sub_child",
		WorkflowID:   wfID,
		State:        model.SubmissionStateCompleted,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{"note": fileObj("ws://"+childWS, "child.txt", childGood)},
		SubmittedBy:  "tester@bvbrc",
		ParentTaskID: "task_proxy",
		CreatedAt:    time.Now().UTC(),
	}
	if err := srv.store.CreateSubmission(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	original := writeOriginal(t, stage, "task_child", "shard.bin", good)
	seedTask(t, srv, child.ID, "task_child", model.ExecutorTypeWorker, map[string]any{
		"shard": fileObj(original, "shard.bin", good),
	})

	_, env := postAs(t, srv, "/api/v1/admin/submissions/"+parent.ID+"/verify-outputs", adminAuthToken)
	rep := decodeReport(t, env)
	if len(rep.Submissions) != 2 || rep.Submissions[1] != child.ID {
		t.Errorf("submissions = %v", rep.Submissions)
	}
	if rep.Summary.Total != 2 || rep.Summary.OK != 1 || rep.Summary.Mismatched != 1 {
		t.Fatalf("verify summary = %+v", rep.Summary)
	}
	bySub := map[string]outputFileReport{}
	for _, fr := range rep.Files {
		bySub[fr.SubmissionID] = fr
	}
	if !bySub[child.ID].OK || bySub[parent.ID].OK {
		t.Errorf("files = %+v", rep.Files)
	}

	_, env = postAs(t, srv, "/api/v1/admin/submissions/"+parent.ID+"/redeliver", adminAuthToken)
	rep = decodeReport(t, env)
	if rep.Summary.OK != 2 || rep.Summary.Reuploaded != 1 {
		t.Fatalf("redeliver summary = %+v files=%+v", rep.Summary, rep.Files)
	}
	for _, fr := range rep.Files {
		if fr.SubmissionID == parent.ID && fr.Source != original[len("file://"):] {
			t.Errorf("parent output re-uploaded from %q, want the child's original", fr.Source)
		}
	}
	if !bytes.Equal(f.Bytes(parentWS), good) {
		t.Errorf("parent output not repaired")
	}
}

// A ws:// output outside the submission's destination is never re-uploaded.
func TestAdminRedeliver_LocationOutsideDestination(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	wfID := createTestWorkflow(t, srv)
	good := binaryPayload(2)
	elsewhere := "/tester@bvbrc/home/results-evil/x.bin" // shares a name prefix with the base
	uploadToFake(t, f, elsewhere, corruptedOf(good))
	loc := writeOriginal(t, stage, "task_e", "x.bin", good)
	sub := seedSubmission(t, srv, wfID, "sub_outside", map[string]any{
		"result": fileObj("ws://"+elsewhere, "x.bin", good),
	}, "delivered")
	seedTask(t, srv, sub.ID, "task_e", model.ExecutorTypeWorker, map[string]any{"result": fileObj(loc, "x.bin", good)})

	_, env := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/redeliver", adminAuthToken)
	rep := decodeReport(t, env)
	if got := rep.Files[0]; got.Action != outputActionFailed || !strings.Contains(got.Error, "outside the submission's output destination") {
		t.Errorf("file = %+v", got)
	}
	if bytes.Equal(f.Bytes(elsewhere), good) {
		t.Errorf("uploaded outside the destination")
	}
}

func TestAdminVerifyOutputs_ExpiredToken(t *testing.T) {
	stage := t.TempDir()
	srv, f := adminOutputsServer(t, true, stage)
	fx := seedDelivered(t, srv, f, stage, "delivered")
	fx.sub.TokenExpiry = time.Now().Add(-time.Hour)
	// TokenExpiry is written on create only; re-create with the past expiry.
	if err := srv.store.DeleteSubmission(context.Background(), fx.sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateSubmission(context.Background(), fx.sub); err != nil {
		t.Fatal(err)
	}

	for _, ep := range []string{"verify-outputs", "redeliver"} {
		w, env := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/"+ep, adminAuthToken)
		if w.Code != http.StatusConflict {
			t.Errorf("%s: status=%d, want 409; body=%s", ep, w.Code, w.Body.String())
		}
		if env.Error == nil || env.Error.Code != model.ErrConflict || !strings.Contains(env.Error.Message, "expired") {
			t.Errorf("%s: error = %+v", ep, env.Error)
		}
	}
}

func TestAdminOutputs_AccessAndAvailability(t *testing.T) {
	t.Run("non-admin is forbidden", func(t *testing.T) {
		stage := t.TempDir()
		srv, f := adminOutputsServer(t, true, stage)
		fx := seedDelivered(t, srv, f, stage, "delivered")
		w, _ := postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/verify-outputs", userAuthToken)
		if w.Code != http.StatusForbidden {
			t.Errorf("status=%d, want 403", w.Code)
		}
		w, _ = postAs(t, srv, "/api/v1/admin/submissions/"+fx.sub.ID+"/redeliver", userAuthToken)
		if w.Code != http.StatusForbidden {
			t.Errorf("status=%d, want 403", w.Code)
		}
	})

	t.Run("unauthenticated is 401", func(t *testing.T) {
		// Later options win: disable the anonymous access testServer enables.
		srv := testServer(WithAnonymousConfig(&AnonymousConfig{Enabled: false}))
		req := httptest.NewRequest("POST", "/api/v1/admin/submissions/sub_x/verify-outputs", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status=%d, want 401", w.Code)
		}
	})

	t.Run("no stager configured", func(t *testing.T) {
		srv, _ := adminOutputsServer(t, false)
		w, env := postAs(t, srv, "/api/v1/admin/submissions/sub_x/verify-outputs", adminAuthToken)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status=%d, want 503", w.Code)
		}
		if env.Error == nil || !strings.Contains(env.Error.Message, "workspace staging not configured") {
			t.Errorf("error = %+v", env.Error)
		}
	})

	t.Run("unknown submission", func(t *testing.T) {
		srv, _ := adminOutputsServer(t, true)
		w, _ := postAs(t, srv, "/api/v1/admin/submissions/sub_missing/verify-outputs", adminAuthToken)
		if w.Code != http.StatusNotFound {
			t.Errorf("status=%d, want 404", w.Code)
		}
	})

	t.Run("no stored token", func(t *testing.T) {
		srv, _ := adminOutputsServer(t, true)
		wfID := createTestWorkflow(t, srv)
		sub := seedSubmission(t, srv, wfID, "sub_notoken", map[string]any{}, "delivered")
		sub.UserToken = ""
		_ = srv.store.DeleteSubmission(context.Background(), sub.ID)
		if err := srv.store.CreateSubmission(context.Background(), sub); err != nil {
			t.Fatal(err)
		}
		w, _ := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/verify-outputs", adminAuthToken)
		if w.Code != http.StatusConflict {
			t.Errorf("status=%d, want 409", w.Code)
		}
	})

	// M2(a): only delivered / upload_failed submissions are accepted, by
	// both endpoints.
	t.Run("output_state not deliverable", func(t *testing.T) {
		srv, _ := adminOutputsServer(t, true)
		wfID := createTestWorkflow(t, srv)
		for _, st := range []string{"", "skipped", "uploading"} {
			sub := seedSubmission(t, srv, wfID, "sub_state_"+st, map[string]any{}, st)
			for _, ep := range []string{"verify-outputs", "redeliver"} {
				w, env := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/"+ep, adminAuthToken)
				if w.Code != http.StatusConflict || env.Error == nil {
					t.Errorf("%s output_state=%q: status=%d, want 409", ep, st, w.Code)
					continue
				}
				want := "only \"delivered\" or \"upload_failed\""
				if st == "uploading" {
					want = "currently being uploaded"
				}
				if !strings.Contains(env.Error.Message, want) {
					t.Errorf("%s output_state=%q: message %q, want containing %q", ep, st, env.Error.Message, want)
				}
			}
		}
	})

	t.Run("non-terminal is refused", func(t *testing.T) {
		srv, _ := adminOutputsServer(t, true)
		wfID := createTestWorkflow(t, srv)
		sub := seedSubmission(t, srv, wfID, "sub_running", map[string]any{}, "delivered")
		sub.State = model.SubmissionStateRunning
		if err := srv.store.UpdateSubmission(context.Background(), sub); err != nil {
			t.Fatal(err)
		}
		w, env := postAs(t, srv, "/api/v1/admin/submissions/"+sub.ID+"/redeliver", adminAuthToken)
		if w.Code != http.StatusConflict || env.Error == nil || !strings.Contains(env.Error.Message, "terminal") {
			t.Errorf("status=%d error=%+v, want 409 terminal", w.Code, env.Error)
		}
	})
}

func TestListSubmissions_OutputStateQuery(t *testing.T) {
	srv, _ := adminOutputsServer(t, true)
	wfID := createTestWorkflow(t, srv)
	seedSubmission(t, srv, wfID, "sub_q_delivered", map[string]any{}, "delivered")
	seedSubmission(t, srv, wfID, "sub_q_failed", map[string]any{}, "upload_failed")
	seedSubmission(t, srv, wfID, "sub_q_none", map[string]any{}, "")

	req := httptest.NewRequest("GET", "/api/v1/submissions/?output_state=delivered,upload_failed", nil)
	req.Header.Set("Authorization", "Bearer "+adminAuthToken)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var env envelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	var subs []map[string]any
	_ = json.Unmarshal(env.Data, &subs)
	if len(subs) != 2 || env.Pagination.Total != 2 {
		t.Fatalf("got %d submissions (total %d), want 2", len(subs), env.Pagination.Total)
	}
	for _, s := range subs {
		if st := s["output_state"]; st != "delivered" && st != "upload_failed" {
			t.Errorf("unexpected output_state %v", st)
		}
	}
}

// S4: like the scheduler's stage-out walker, secondaryFiles are not visited.
func TestWalkOutputFiles(t *testing.T) {
	tree := map[string]any{
		"single": map[string]any{"class": "File", "location": "ws:///a/one", "secondaryFiles": []any{
			map[string]any{"class": "File", "location": "ws:///a/one.idx"},
		}},
		"array": []any{
			map[string]any{"class": "File", "location": "ws:///a/two"},
			[]any{map[string]any{"class": "File", "location": "ws:///a/three"}},
		},
		"dir": map[string]any{"class": "Directory", "basename": "top", "listing": []any{
			map[string]any{"class": "File", "location": "ws:///a/top/four"},
			map[string]any{"class": "Directory", "basename": "inner", "listing": []any{
				map[string]any{"class": "File", "location": "ws:///a/top/inner/five"},
			}},
		}},
		"record": map[string]any{"nested": map[string]any{"class": "File", "location": "file:///six"}},
		"scalar": 42,
		"null":   nil,
	}
	got := map[string]string{}
	walkOutputFiles(tree, "", func(obj map[string]any, subPath string) {
		got[obj["location"].(string)] = subPath
	})
	want := map[string]string{
		"ws:///a/one":            "",
		"ws:///a/two":            "",
		"ws:///a/three":          "",
		"ws:///a/top/four":       "top",
		"ws:///a/top/inner/five": "top/inner",
		"file:///six":            "",
	}
	if _, visited := got["ws:///a/one.idx"]; visited {
		t.Errorf("secondaryFiles must not be visited")
	}
	if len(got) != len(want) {
		t.Fatalf("visited %v, want %v", got, want)
	}
	for loc, sp := range want {
		if g, ok := got[loc]; !ok || g != sp {
			t.Errorf("%s: subPath = %q (visited=%v), want %q", loc, g, ok, sp)
		}
	}
}

func TestPathUnderAny(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "sub", "f.txt")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sibling := base + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(sibling, "f.txt")
	if err := os.WriteFile(evil, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link.txt")
	if err := os.Symlink(evil, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		p    string
		dirs []string
		want bool
	}{
		{"inside", inside, []string{base}, true},
		{"the dir itself", base, []string{base}, true},
		{"sibling with shared prefix", evil, []string{base}, false},
		{"symlink escaping", link, []string{base}, false},
		{"no dirs", inside, nil, false},
		{"missing path", filepath.Join(base, "nope"), []string{base}, false},
		{"second dir matches", evil, []string{base, sibling}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pathUnderAny(tt.p, tt.dirs); got != tt.want {
				t.Errorf("pathUnderAny(%s) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}
