//go:build integration

package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/me/gowe/internal/bvbrc"
	bvbrcpkg "github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// skipIfNoBVBRC skips the test if no BV-BRC token is available or expired.
func skipIfNoBVBRC(t *testing.T) (bvbrc.RPCCaller, string) {
	t.Helper()
	tok, err := bvbrc.ResolveToken()
	if err != nil {
		t.Skipf("no BV-BRC token: %v", err)
	}
	info := bvbrc.ParseToken(tok)
	if info.IsExpired() {
		t.Skip("BV-BRC token is expired")
	}

	cfg := bvbrc.DefaultClientConfig()
	cfg.Token = tok
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	caller := bvbrc.NewHTTPRPCCaller(cfg, logger)
	return caller, info.Username
}

// cleanupBVBRCResultFolder best-effort deletes the job-result object and its
// hidden result folder a Date submission created (outputPath+"/"+outputFile
// and outputPath+"/."+outputFile — see docs/BVBRC-App-Output-Convention.md),
// via the existing pkg/bvbrc.Client.WorkspaceDelete helper (no new delete
// logic). Re-resolves its own token so callers don't need to thread one
// through just for cleanup; a failure here is logged, not fatal.
func cleanupBVBRCResultFolder(t *testing.T, outputPath, outputFile string) {
	t.Helper()
	tok, err := bvbrc.ResolveToken()
	if err != nil {
		return // nothing to clean up without a token
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := bvbrcpkg.NewClient(bvbrcpkg.Config{
		WorkspaceURL: bvbrcpkg.DefaultWorkspaceURL,
		Token:        tok,
		Timeout:      30 * time.Second,
	}, nil)

	objects := []string{
		outputPath + "/" + outputFile,
		outputPath + "/." + outputFile,
	}
	if err := client.WorkspaceDelete(ctx, bvbrcpkg.WorkspaceDeleteInput{
		Objects: objects, Force: true, DeleteDirectories: true,
	}); err != nil {
		t.Logf("cleanup: workspace delete %v failed (non-fatal): %v", objects, err)
	}
}

// TestBVBRCIntegration_SubmitAndPoll submits a small Date job to BV-BRC
// and polls until it reaches a terminal state.
//
// output_path/output_file MUST be set as actual app parameters (keys in
// task.Inputs, which Submit() copies verbatim into the params map it sends
// to start_app), not just inferred by the executor: Submit() only reads
// params["output_path"] to pick the workspace argument it passes to
// start_app positionally, but the app itself ALSO reads output_path and
// output_file out of its own parameter map to build its result folder as
// output_path + "/." + output_file. Leaving either unset there sends the app
// an empty string for that half of the path, which BV-BRC's WorkspaceImpl.pm
// rejects outright ("/. is not a valid object path!") — caught by the live
// #154/#198 promotion round-trip, where this test (previously submitting
// with empty Inputs) was failing at BV-BRC for that reason.
func TestBVBRCIntegration_SubmitAndPoll(t *testing.T) {
	caller, username := skipIfNoBVBRC(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec := NewBVBRCExecutor(bvbrc.DefaultAppServiceURL, caller, logger)

	outputPath := "/" + username + "/home/gowe-test"
	outputFile := fmt.Sprintf("gowe-integ-%d", time.Now().UnixNano())

	task := &model.Task{
		ID:         "integ_task_1",
		BVBRCAppID: "Date",
		Inputs: map[string]any{
			"output_path": outputPath,
			"output_file": outputFile,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Cleanup(func() {
		cleanupBVBRCResultFolder(t, outputPath, outputFile)
	})

	extID, err := exec.Submit(ctx, task)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	t.Logf("submitted job: %s", extID)
	task.ExternalID = extID

	// Poll until terminal.
	for {
		state, err := exec.Status(ctx, task)
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}
		t.Logf("state: %s", state)
		if state.IsTerminal() {
			if state != model.TaskStateSuccess {
				t.Errorf("job ended with state %s, want SUCCESS", state)
			}
			break
		}
		time.Sleep(10 * time.Second)
	}

	// Fetch logs.
	stdout, stderr, err := exec.Logs(ctx, task)
	if err != nil {
		t.Errorf("Logs failed: %v", err)
	}
	t.Logf("stdout: %s", stdout)
	if stderr != "" {
		t.Logf("stderr: %s", stderr)
	}
}

// TestBVBRCIntegration_WorkspaceDirectoryRoundTrip is the promotion evidence
// for #154's "no recursive directory download" gap and #198's ws:// staging
// promotion: a small local tree (including a nested subfolder) is uploaded
// file-by-file into a fresh workspace folder via pkg/staging.WorkspaceStager
// (the same stager server-side pre/post-staging uses), then recursively
// downloaded back down with a single StageIn call, and the two trees are
// compared byte-for-byte.
//
// This is a stager-only round-trip (Role B of docs/BVBRC-Workspace-Deep-Dive.md
// §3) rather than a full CWL submission: recursive stage-in has no app-level
// shape to hang it on (no stock BV-BRC app both accepts and produces a
// multi-file Directory through a CWL tool GoWe already wraps), and the code
// path being verified — WorkspaceStager.StageIn's folder-vs-file detection
// and recursive walk — is exactly the same whichever CWL step drives it.
func TestBVBRCIntegration_WorkspaceDirectoryRoundTrip(t *testing.T) {
	tok, err := bvbrc.ResolveToken()
	if err != nil {
		t.Skipf("no BV-BRC token: %v", err)
	}
	info := bvbrc.ParseToken(tok)
	if info.IsExpired() {
		t.Skip("BV-BRC token is expired")
	}

	stager := staging.NewWorkspaceStager(staging.WorkspaceConfig{
		Token: tok,
	}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	root := fmt.Sprintf("/%s/home/gowe-test/roundtrip-%d", info.Username, time.Now().UnixNano())
	t.Cleanup(func() {
		// Best-effort recursive delete of the scratch folder this test
		// created, via the raw pkg/bvbrc client (WorkspaceStager exposes no
		// delete of its own).
		delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer delCancel()
		client := bvbrcpkg.NewClient(bvbrcpkg.Config{
			WorkspaceURL: bvbrcpkg.DefaultWorkspaceURL,
			Token:        tok,
			Timeout:      30 * time.Second,
		}, nil)
		if err := client.WorkspaceDelete(delCtx, bvbrcpkg.WorkspaceDeleteInput{
			Objects: []string{root}, Force: true, DeleteDirectories: true,
		}); err != nil {
			t.Logf("cleanup: workspace delete %s failed (non-fatal): %v", root, err)
		}
	})

	// Build a small local source tree.
	srcRoot := t.TempDir()
	files := map[string]string{
		"summary.txt":     "top level file",
		"contigs/1.fasta": ">seq1\nACGT",
		"contigs/2.fasta": ">seq2\nTTTT",
	}
	for rel, content := range files {
		p := filepath.Join(srcRoot, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// Upload each file individually; ensureDir creates the intermediate
	// "contigs" folder on the way, exercising the same nested-folder
	// creation path prestage/poststage use in production.
	//
	// filepath.Dir returns "." for a root-level file with no subdirectory
	// (e.g. "summary.txt"); naively appending that to destDir would create a
	// real workspace folder literally named "." and upload the file inside
	// it instead of at root — which is exactly the bug the live promotion
	// run caught (StageIn correctly refuses to descend into an entry named
	// ".", since blindly filepath.Joining it would silently collapse to the
	// parent directory rather than a real child). Root-level files go
	// straight into destRoot with no subpath appended.
	for rel := range files {
		src := filepath.Join(srcRoot, rel)
		destDir := root
		if sub := filepath.ToSlash(filepath.Dir(rel)); sub != "." {
			destDir = root + "/" + sub
		}
		if _, err := stager.StageOut(ctx, src, "roundtrip-task", staging.StageOptions{
			Metadata: map[string]string{"destination": destDir},
		}); err != nil {
			t.Fatalf("StageOut %s: %v", rel, err)
		}
	}

	// Recursively download the whole tree back down with one StageIn call.
	destRoot := filepath.Join(t.TempDir(), "downloaded")
	if err := stager.StageIn(ctx, "ws://"+root, destRoot, staging.StageOptions{}); err != nil {
		t.Fatalf("StageIn (recursive directory): %v", err)
	}

	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(destRoot, rel))
		if err != nil {
			t.Errorf("read downloaded %s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", rel, got, want)
		}
	}
}

// TestBVBRCIntegration_WildcardGlobOutputResolution is the promotion
// evidence for #154's "wildcard globs unresolved" gap: it submits the same
// Date app TestBVBRCIntegration_SubmitAndPoll uses, waits for the real
// result folder to exist, then calls the unexported buildOutputsFromGlobs
// directly against it with a synthetic tool definition carrying one
// wildcard-glob array output ("*") and one wildcard-glob scalar output
// ("date.*"). Calling it directly — rather than depending on whatever
// Date's own CWL wrapper declares — sidesteps needing an app whose
// output_files BV-BRC happens to leave empty (the condition that triggers
// this code path in production) and lets this test control both the glob
// shape and the type (array vs scalar) being verified.
func TestBVBRCIntegration_WildcardGlobOutputResolution(t *testing.T) {
	caller, username := skipIfNoBVBRC(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	exec := NewBVBRCExecutor(bvbrc.DefaultAppServiceURL, caller, logger)

	tok, err := bvbrc.ResolveToken()
	if err != nil {
		t.Skipf("no BV-BRC token: %v", err)
	}

	// output_path AND output_file must both be set as actual app parameters
	// (task.Inputs, which Submit() copies verbatim into the params map sent
	// to start_app) — the app builds its result folder from its OWN
	// parameter map as output_path + "/." + output_file, independently of
	// the workspace argument Submit() derives from params["output_path"].
	// Leaving output_file unset (as this test previously did) sends the app
	// an empty string for that half of the path, which BV-BRC's
	// WorkspaceImpl.pm rejects outright ("/. is not a valid object path!") —
	// caught by the live #154/#198 promotion round-trip. output_file is
	// unique per run so repeated test runs don't collide on the same result
	// folder.
	outputPath := "/" + username + "/home/gowe-test"
	outputFile := fmt.Sprintf("gowe-integ-%d", time.Now().UnixNano())

	task := &model.Task{
		ID:         "integ_task_wildcard",
		BVBRCAppID: "Date",
		Inputs: map[string]any{
			"output_path": outputPath,
			"output_file": outputFile,
		},
		RuntimeHints: &model.RuntimeHints{
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Token: tok},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Cleanup(func() {
		cleanupBVBRCResultFolder(t, outputPath, outputFile)
	})

	extID, err := exec.Submit(ctx, task)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	task.ExternalID = extID

	for {
		state, err := exec.Status(ctx, task)
		if err != nil {
			t.Fatalf("Status failed: %v", err)
		}
		if state.IsTerminal() {
			if state != model.TaskStateSuccess {
				t.Fatalf("job ended with state %s, want SUCCESS", state)
			}
			break
		}
		time.Sleep(10 * time.Second)
	}
	t.Logf("result folder: %s/.%s (user %s)", outputPath, outputFile, username)

	task.Tool = map[string]any{
		"outputs": map[string]any{
			"result_folder": map[string]any{
				"type":          "Directory",
				"outputBinding": map[string]any{"glob": "."},
			},
			"all_files": map[string]any{
				"type":          "File[]",
				"outputBinding": map[string]any{"glob": "*"},
			},
			"date_file": map[string]any{
				"type":          "File",
				"outputBinding": map[string]any{"glob": "date.*"},
			},
		},
	}

	outputs, err := exec.buildOutputsFromGlobs(ctx, task, outputPath, outputFile)
	if err != nil {
		t.Fatalf("buildOutputsFromGlobs: %v", err)
	}
	t.Logf("resolved outputs: %#v", outputs)

	arr, ok := outputs["all_files"].([]any)
	if !ok {
		t.Fatalf("outputs[all_files] = %#v (%T), want a non-empty array (empty listing is tolerated, but Date's result folder should be indexed by now)", outputs["all_files"], outputs["all_files"])
	}
	if len(arr) == 0 {
		t.Error("outputs[all_files] resolved to an empty array")
	}
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok || obj["class"] != "File" || obj["location"] == nil {
			t.Errorf("all_files entry = %#v, want a File object with a location", item)
		}
	}
}
