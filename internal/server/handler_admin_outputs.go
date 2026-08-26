package server

// Admin output verification and re-delivery.
//
// Outputs delivered to the BV-BRC Workspace before #174 went through the
// inline JSON path, which corrupts every binary file. The worker records
// `checksum` ("sha1$<hex>") and `size` on each output File before any upload,
// so the recorded values are the ground truth for what SHOULD be in the
// workspace, and the task rows keep `file://` locations of the originals on
// the shared stage-out directory. These endpoints let an admin:
//
//   - verify-outputs: download every ws:// output and compare it to the
//     recorded checksum and size (read-only);
//   - redeliver: for every output that does not verify, find the local
//     original by checksum+size in the task outputs (basenames are not unique
//     across scatter tasks), re-upload it through the verified streaming path
//     to the same workspace path, re-verify, and update the submission.
//
// Both use the submission's STORED token: the admin's own token has no write
// permission in another user's home, so it is deliberately not a fallback.
// Child submissions (sub-workflow fan-out) are included at every depth.
//
// Local originals are only ever read from under --redeliver-source-dirs
// (symlinks resolved, segment-aware prefix), only when they are regular files
// of the recorded size, and never from FIFOs or devices.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

const (
	outputStateDelivered    = "delivered"
	outputStateUploadFailed = "upload_failed"
	outputStateUploading    = "uploading"
	outputStagingFailedCode = "OUTPUT_STAGING_FAILED"

	// verifyDownloadTimeout bounds one verification download end to end.
	// The verifier streams into a hasher, so a healthy download of any size
	// finishes long before this; it only catches a Shock that stalls
	// mid-body, which ResponseHeaderTimeout alone cannot see.
	verifyDownloadTimeout = time.Hour
	// verifyResponseHeaderTimeout bounds the wait for the download's headers.
	verifyResponseHeaderTimeout = 5 * time.Minute
)

// Per-file actions reported by redeliver.
const (
	outputActionVerified        = "verified"         // already matched the recorded checksum
	outputActionReuploaded      = "reuploaded"       // re-uploaded and re-verified OK
	outputActionWouldReupload   = "would_reupload"   // dry run: original found, nothing changed
	outputActionOriginalMissing = "original_missing" // no usable local original with that checksum+size
	outputActionFailed          = "failed"           // re-upload or re-verify failed, or not recoverable
)

// outputFileReport is the per-file result of a verification or re-delivery.
type outputFileReport struct {
	SubmissionID     string `json:"submission_id"`
	Location         string `json:"location"`
	ExpectedChecksum string `json:"expected_checksum,omitempty"`
	ActualChecksum   string `json:"actual_checksum,omitempty"`
	ExpectedSize     int64  `json:"expected_size"`
	ActualSize       int64  `json:"actual_size"`
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`

	// Redeliver-only fields.
	Action string `json:"action,omitempty"`
	Source string `json:"source,omitempty"` // local original used (or that would be used)

	ref *outputFileRef // internal
}

// outputReportSummary aggregates the per-file outcomes.
type outputReportSummary struct {
	Total           int `json:"total"`
	OK              int `json:"ok"`
	Mismatched      int `json:"mismatched"`
	Errors          int `json:"errors"`
	Reuploaded      int `json:"reuploaded,omitempty"`
	WouldReupload   int `json:"would_reupload,omitempty"`
	OriginalMissing int `json:"original_missing,omitempty"`
	Failed          int `json:"failed,omitempty"`
}

// outputReport is the response body of both endpoints.
type outputReport struct {
	SubmissionID     string              `json:"submission_id"`
	State            string              `json:"state"`
	OutputState      string              `json:"output_state"`
	DryRun           bool                `json:"dry_run,omitempty"`
	Submissions      []string            `json:"submissions"` // root + descendants examined
	Files            []outputFileReport  `json:"files"`
	Summary          outputReportSummary `json:"summary"`
	Updated          bool                `json:"updated,omitempty"`           // submission row(s) written
	StateRestored    bool                `json:"state_restored,omitempty"`    // FAILED(OUTPUT_STAGING_FAILED) → COMPLETED
	ManifestUploaded bool                `json:"manifest_uploaded,omitempty"` // _gowe_outputs.json rewritten
	ManifestError    string              `json:"manifest_error,omitempty"`
}

// outputFileRef is one File object found in a submission's output tree.
type outputFileRef struct {
	sub      *model.Submission
	obj      map[string]any
	location string
	subPath  string // Directory nesting below the output destination
	base     string // workspace path every re-upload for this file must stay under
}

// subExpect is the (state, output_state) pair a submission had when the
// request loaded it; every write is compare-and-set against it.
type subExpect struct {
	state       model.SubmissionState
	outputState string
}

// handleAdminVerifyOutputs downloads every ws:// output File of the submission
// (and its descendants) and compares it to the recorded checksum and size.
// POST /api/v1/admin/submissions/{id}/verify-outputs
func (s *Server) handleAdminVerifyOutputs(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	ctx := r.Context()

	root, token, ok := s.loadOutputRecoveryTarget(w, r)
	if !ok {
		return
	}

	subs, err := s.collectOutputTree(ctx, root)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	refs := collectOutputFileRefs(root, subs)

	files := s.verifyOutputFiles(ctx, token, refs)
	respondOK(w, reqID, buildOutputReport(root, subs, files, false))
}

// handleAdminRedeliverOutputs re-uploads every output that does not verify
// from its local original and re-verifies it.
// POST /api/v1/admin/submissions/{id}/redeliver?dry_run=true
func (s *Server) handleAdminRedeliverOutputs(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	ctx := r.Context()
	dryRun := r.URL.Query().Get("dry_run") == "true"
	id := chi.URLParam(r, "id")

	// One re-delivery per submission at a time; the lock is in-memory so a
	// crash mid-request never leaves a stuck marker in the database.
	if _, busy := s.redeliverLocks.LoadOrStore(id, struct{}{}); busy {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "re-delivery in progress for this submission",
		})
		return
	}
	defer s.redeliverLocks.Delete(id)

	root, token, ok := s.loadOutputRecoveryTarget(w, r)
	if !ok {
		return
	}

	subs, err := s.collectOutputTree(ctx, root)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	// Snapshot the loaded states before anything below mutates them.
	expect := map[string]subExpect{}
	for _, sub := range subs {
		expect[sub.ID] = subExpect{state: sub.State, outputState: sub.OutputState}
	}
	refs := collectOutputFileRefs(root, subs)

	files := s.verifyOutputFiles(ctx, token, refs)

	// Local originals, keyed by checksum+size, from every task in the tree.
	index := s.buildOriginalIndex(ctx, subs)

	changed := map[string]*model.Submission{}
	for i := range files {
		f := &files[i]
		if f.OK {
			f.Action = outputActionVerified
			continue
		}
		s.redeliverOutputFile(ctx, token, f, index, dryRun, changed)
	}

	report := buildOutputReport(root, subs, files, dryRun)
	if dryRun {
		respondOK(w, reqID, report)
		return
	}

	allOK := report.Summary.Total > 0 && report.Summary.OK == report.Summary.Total
	rootDirty := changed[root.ID] != nil
	if allOK && root.OutputState != outputStateDelivered {
		root.OutputState = outputStateDelivered
		rootDirty = true
		// The scheduler fails a submission whose delivery failed; the work
		// itself completed, so a successful re-delivery restores it.
		if root.State == model.SubmissionStateFailed && root.Error != nil && root.Error.Code == outputStagingFailedCode {
			root.State = model.SubmissionStateCompleted
			root.Error = nil
			report.StateRestored = true
		}
	}

	// Persist: children first, root last, each compare-and-set against the
	// state it had when this request loaded it.
	for _, sub := range subs {
		if sub.ID == root.ID {
			continue
		}
		if changed[sub.ID] == nil {
			continue
		}
		if !s.persistRedelivered(w, reqID, ctx, sub, expect[sub.ID]) {
			return
		}
		report.Updated = true
	}
	if rootDirty {
		if !s.persistRedelivered(w, reqID, ctx, root, expect[root.ID]) {
			return
		}
		report.Updated = true
		s.logger.Info("redeliver: submission updated",
			"submission_id", root.ID,
			"output_state", root.OutputState,
			"state", root.State,
			"reuploaded", report.Summary.Reuploaded,
		)

		// The manifest names every output with its location; rewrite it so it
		// reflects the re-delivered tree. Non-fatal: the files are already
		// verified in place.
		if base, ok := wsPathOf(root.OutputDestination); ok {
			stager := s.wsStager.WithToken(token)
			if dest, err := staging.UploadOutputManifest(ctx, stager, root, base); err != nil {
				s.logger.Warn("redeliver: upload output manifest failed", "submission_id", root.ID, "error", err)
				report.ManifestError = err.Error()
			} else {
				s.logger.Info("redeliver: uploaded output manifest", "submission_id", root.ID, "path", dest)
				report.ManifestUploaded = true
			}
		}
	}
	report.State = string(root.State)
	report.OutputState = root.OutputState

	respondOK(w, reqID, report)
}

// persistRedelivered writes sub with compare-and-set against expect and
// answers 409 (returning false) when the row moved on since it was loaded.
func (s *Server) persistRedelivered(w http.ResponseWriter, reqID string, ctx context.Context, sub *model.Submission, expect subExpect) bool {
	applied, err := s.store.UpdateSubmissionIfState(ctx, sub, expect.state, expect.outputState)
	if err != nil {
		s.logger.Error("redeliver: update submission", "submission_id", sub.ID, "error", err)
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return false
	}
	if !applied {
		s.logger.Warn("redeliver: submission changed concurrently; not written",
			"submission_id", sub.ID, "expected_state", expect.state, "expected_output_state", expect.outputState)
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: fmt.Sprintf("submission %s changed concurrently (expected state=%s output_state=%q); nothing written, re-run to retry", sub.ID, expect.state, expect.outputState),
		})
		return false
	}
	return true
}

// loadOutputRecoveryTarget resolves the submission and its stored token,
// writing the error response itself when the request cannot proceed. Both
// endpoints accept only submissions whose delivery already ran to an outcome
// (output_state delivered or upload_failed): anything else has either not
// been delivered by this server or is being delivered right now.
func (s *Server) loadOutputRecoveryTarget(w http.ResponseWriter, r *http.Request) (*model.Submission, string, bool) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	if s.wsStager == nil {
		respondError(w, reqID, http.StatusServiceUnavailable, &model.APIError{
			Code:    model.ErrInternal,
			Message: "workspace staging not configured on this server",
		})
		return nil, "", false
	}

	sub, err := s.store.GetSubmission(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return nil, "", false
	}
	if sub == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", id))
		return nil, "", false
	}

	switch sub.OutputState {
	case outputStateDelivered, outputStateUploadFailed:
	case outputStateUploading:
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "submission outputs are currently being uploaded by the scheduler; retry later",
		})
		return nil, "", false
	default:
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: fmt.Sprintf("submission output_state is %q; only \"delivered\" or \"upload_failed\" submissions can be verified or re-delivered", sub.OutputState),
		})
		return nil, "", false
	}
	if !sub.State.IsTerminal() {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: fmt.Sprintf("submission is %s; outputs can only be verified or re-delivered once it is terminal", sub.State),
		})
		return nil, "", false
	}

	// The stored submission token is the only credential that can read and
	// write the owner's workspace; the admin's own token is not a fallback.
	if sub.UserToken == "" {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "submission has no stored user token; the workspace cannot be accessed on the owner's behalf",
		})
		return nil, "", false
	}
	if !sub.TokenExpiry.IsZero() && time.Now().After(sub.TokenExpiry) {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code: model.ErrConflict,
			Message: fmt.Sprintf("stored user token expired at %s; the owner must re-submit (or re-authenticate) before outputs can be verified or re-delivered",
				sub.TokenExpiry.UTC().Format(time.RFC3339)),
		})
		return nil, "", false
	}

	return sub, sub.UserToken, true
}

// collectOutputTree returns the root submission followed by every descendant
// child submission (spawned via sub-workflow proxy tasks), each loaded in
// full through GetSubmission. The visited set guards against cycles.
func (s *Server) collectOutputTree(ctx context.Context, root *model.Submission) ([]*model.Submission, error) {
	visited := map[string]bool{root.ID: true}
	out := []*model.Submission{root}

	var walk func(sub *model.Submission) error
	walk = func(sub *model.Submission) error {
		tasks, err := s.store.ListTasksBySubmission(ctx, sub.ID)
		if err != nil {
			return fmt.Errorf("list tasks for %s: %w", sub.ID, err)
		}
		for _, task := range tasks {
			if task.ExecutorType != model.ExecutorTypeSubworkflow {
				continue
			}
			children, err := s.store.GetChildSubmissions(ctx, task.ID)
			if err != nil {
				return fmt.Errorf("list children of task %s: %w", task.ID, err)
			}
			for _, child := range children {
				if visited[child.ID] {
					continue
				}
				visited[child.ID] = true
				// GetChildSubmissions returns a partial row; load the full one
				// so a later UpdateSubmission writes every column faithfully.
				full, err := s.store.GetSubmission(ctx, child.ID)
				if err != nil {
					return fmt.Errorf("load child submission %s: %w", child.ID, err)
				}
				if full == nil {
					continue
				}
				out = append(out, full)
				if err := walk(full); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return out, nil
}

// collectOutputFileRefs walks every submission's output tree and returns the
// File objects that matter for delivery: every ws:// File, plus file:// Files
// on submissions that have an output destination (those were never delivered).
// Child submissions carry no destination, so their file:// outputs — which the
// parent gathers and delivers — are not reported twice. Each ref records the
// workspace base every re-upload must stay under: the submission's own
// destination, or the root's for children.
func collectOutputFileRefs(root *model.Submission, subs []*model.Submission) []*outputFileRef {
	rootBase, _ := wsPathOf(root.OutputDestination)
	var refs []*outputFileRef
	for _, sub := range subs {
		base := rootBase
		if b, ok := wsPathOf(sub.OutputDestination); ok {
			base = b
		}
		walkOutputFiles(sub.Outputs, "", func(obj map[string]any, subPath string) {
			loc, _ := obj["location"].(string)
			switch {
			case strings.HasPrefix(loc, "ws://"):
			case strings.HasPrefix(loc, "file://") && sub.OutputDestination != "":
			default:
				return
			}
			refs = append(refs, &outputFileRef{sub: sub, obj: obj, location: loc, subPath: subPath, base: base})
		})
	}
	return refs
}

// walkOutputFiles visits every File object in a CWL output value at any
// nesting (maps, arrays, Directory listings), tracking the Directory path
// below the destination the same way the scheduler's stage-out does, so a
// re-upload lands where the original delivery did. Like the scheduler's
// stageFileInTree it does NOT descend into secondaryFiles: those are never
// delivered on their own, so reporting them would only produce noise.
func walkOutputFiles(v any, subPath string, fn func(obj map[string]any, subPath string)) {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		switch class {
		case "File":
			fn(val, subPath)
		case "Directory":
			childSubPath := subPath
			if basename, ok := val["basename"].(string); ok && basename != "" {
				if childSubPath != "" {
					childSubPath += "/" + basename
				} else {
					childSubPath = basename
				}
			}
			if listing, ok := val["listing"]; ok {
				walkOutputFiles(listing, childSubPath, fn)
			}
		default:
			for _, item := range val {
				walkOutputFiles(item, subPath, fn)
			}
		}
	case []any:
		for _, item := range val {
			walkOutputFiles(item, subPath, fn)
		}
	}
}

// wsVerifier downloads workspace objects on behalf of the submission owner.
type wsVerifier struct {
	client *bvbrc.Client
	http   *http.Client
	token  string
}

var (
	verifyTransportOnce   sync.Once
	sharedVerifyTransport *http.Transport
)

// verifyTransport is the process-wide download transport: the default
// transport with a bounded wait for response headers, shared so verifier
// instances (one per request) pool connections.
func verifyTransport() *http.Transport {
	verifyTransportOnce.Do(func() {
		tr, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
		}
		tr = tr.Clone()
		tr.ResponseHeaderTimeout = verifyResponseHeaderTimeout
		tr.IdleConnTimeout = 90 * time.Second
		sharedVerifyTransport = tr
	})
	return sharedVerifyTransport
}

func (s *Server) newWSVerifier(token string) *wsVerifier {
	cfg := s.wsStager.Config()
	return &wsVerifier{
		client: bvbrc.NewClient(bvbrc.Config{
			WorkspaceURL: cfg.WorkspaceURL,
			Token:        token,
			Timeout:      cfg.Timeout,
		}, s.logger),
		// No total client timeout: the body streams into a hasher for as
		// long as it takes. Headers are bounded by the transport and the
		// whole download by the per-request deadline in hashDownload.
		http:  &http.Client{Transport: verifyTransport()},
		token: token,
	}
}

// downloadURLs resolves download URLs for the given workspace paths in one
// RPC; a missing object or a folder yields "".
func (v *wsVerifier) downloadURLs(ctx context.Context, paths []string) (map[string]string, error) {
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	return v.client.WorkspaceGetDownloadURL(ctx, paths)
}

// hashDownload streams the object at url through a SHA-1 hasher and returns
// the CWL-style checksum and the byte count.
func (v *wsVerifier) hashDownload(ctx context.Context, url string) (string, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, verifyDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "OAuth "+v.token)

	resp, err := v.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, fmt.Errorf("download HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	h := sha1.New()
	n, err := io.Copy(h, resp.Body)
	if err != nil {
		return "", n, fmt.Errorf("read body: %w", err)
	}
	return "sha1$" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// verifyOutputFiles compares each referenced File to its recorded checksum
// and size. It is read-only.
func (s *Server) verifyOutputFiles(ctx context.Context, token string, refs []*outputFileRef) []outputFileReport {
	verifier := s.newWSVerifier(token)

	var wsPaths []string
	for _, ref := range refs {
		if p, ok := wsPathOf(ref.location); ok {
			wsPaths = append(wsPaths, p)
		}
	}
	urls, urlErr := verifier.downloadURLs(ctx, wsPaths)

	files := make([]outputFileReport, 0, len(refs))
	for _, ref := range refs {
		f := outputFileReport{
			SubmissionID:     ref.sub.ID,
			Location:         ref.location,
			ExpectedChecksum: stringField(ref.obj, "checksum"),
			ExpectedSize:     int64Field(ref.obj, "size"),
			ref:              ref,
		}
		s.verifyOne(ctx, verifier, &f, urls, urlErr)
		files = append(files, f)
	}
	return files
}

// verifyOne fills in the actual checksum/size and the ok/error outcome of f.
// urls/urlErr are the batched get_download_url result for all ws:// refs.
func (s *Server) verifyOne(ctx context.Context, verifier *wsVerifier, f *outputFileReport, urls map[string]string, urlErr error) {
	f.OK = false
	f.ActualChecksum = ""
	f.ActualSize = 0
	f.Error = ""

	wsPath, isWS := wsPathOf(f.Location)
	if !isWS {
		if strings.HasPrefix(f.Location, "file://") {
			f.Error = "not delivered: output still has a local file:// location"
		} else {
			f.Error = "unsupported location scheme"
		}
		return
	}
	if f.ExpectedChecksum == "" {
		f.Error = "no recorded checksum to compare against"
		return
	}
	if urlErr != nil {
		f.Error = "get download URL: " + urlErr.Error()
		return
	}
	url := urls[wsPath]
	if url == "" {
		f.Error = "no download URL: object is missing from the workspace (or is a folder)"
		return
	}

	sum, n, err := verifier.hashDownload(ctx, url)
	if err != nil {
		f.Error = err.Error()
		return
	}
	f.ActualChecksum = sum
	f.ActualSize = n

	switch {
	case sum != f.ExpectedChecksum:
		f.Error = "checksum mismatch"
	case f.ExpectedSize > 0 && n != f.ExpectedSize:
		f.Error = fmt.Sprintf("size mismatch: workspace holds %d bytes, expected %d", n, f.ExpectedSize)
	default:
		f.OK = true
	}
}

// originalKey keys local originals by the recorded checksum and size.
func originalKey(checksum string, size int64) string {
	return checksum + "|" + fmt.Sprint(size)
}

// checkCandidate decides, without opening it, whether a local path may be
// read as an original: it must resolve inside --redeliver-source-dirs, be a
// regular file (never a FIFO, device or directory, which could block or leak)
// and have the recorded size. Returns a reason when it must be skipped.
func (s *Server) checkCandidate(p string, expectedSize int64) string {
	if len(s.redeliverSourceDirs) == 0 {
		return "file:// recovery refused: the server has no --redeliver-source-dirs configured"
	}
	if !pathUnderAny(p, s.redeliverSourceDirs) {
		return fmt.Sprintf("%s is outside --redeliver-source-dirs", p)
	}
	st, err := os.Stat(p)
	if err != nil {
		return err.Error()
	}
	if !st.Mode().IsRegular() {
		return fmt.Sprintf("%s is not a regular file (%s)", p, st.Mode().Type())
	}
	if expectedSize > 0 && st.Size() != expectedSize {
		return fmt.Sprintf("%s has size %d, expected %d", p, st.Size(), expectedSize)
	}
	return ""
}

// originalIndex maps checksum+size → an admissible local path, and keeps
// the reasons candidates for a key were rejected so a per-file "original
// missing" outcome can say why.
type originalIndex struct {
	paths   map[string]string
	skipped map[string][]string
}

// buildOriginalIndex indexes every file:// output File recorded on any task
// of the given submissions by checksum+size (scatter shards share basenames,
// so the name is never used for matching). Sub-workflow proxy tasks mirror
// their child's outputs, so a file may be seen twice; the first admissible
// path wins. Only candidates that pass checkCandidate are indexed.
func (s *Server) buildOriginalIndex(ctx context.Context, subs []*model.Submission) *originalIndex {
	index := &originalIndex{paths: map[string]string{}, skipped: map[string][]string{}}
	if len(s.redeliverSourceDirs) == 0 {
		return index
	}
	for _, sub := range subs {
		tasks, err := s.store.ListTasksBySubmission(ctx, sub.ID)
		if err != nil {
			s.logger.Warn("redeliver: list tasks for original index", "submission_id", sub.ID, "error", err)
			continue
		}
		for _, task := range tasks {
			walkOutputFiles(task.Outputs, "", func(obj map[string]any, _ string) {
				local := localPathOf(obj)
				if local == "" {
					return
				}
				checksum := stringField(obj, "checksum")
				if checksum == "" {
					return
				}
				size := int64Field(obj, "size")
				key := originalKey(checksum, size)
				if _, seen := index.paths[key]; seen {
					return
				}
				if reason := s.checkCandidate(local, size); reason != "" {
					s.logger.Debug("redeliver: candidate skipped", "task_id", task.ID, "path", local, "reason", reason)
					index.skipped[key] = append(index.skipped[key], reason)
					return
				}
				index.paths[key] = local
			})
		}
	}
	return index
}

// redeliverOutputFile recovers one failed output: locate the original, verify
// it locally, re-upload it to the same workspace path, and re-verify.
func (s *Server) redeliverOutputFile(ctx context.Context, token string, f *outputFileReport, index *originalIndex, dryRun bool, changed map[string]*model.Submission) {
	ref := f.ref
	if f.ExpectedChecksum == "" {
		f.Action = outputActionFailed
		f.Error = "no recorded checksum; cannot re-deliver safely"
		return
	}

	// Where the object must live — always inside the workspace base.
	var destDir, name string
	if wsPath, ok := wsPathOf(f.Location); ok {
		wsPath = path.Clean(wsPath)
		destDir, name = path.Dir(wsPath), path.Base(wsPath)
	} else if strings.HasPrefix(f.Location, "file://") {
		if ref.base == "" {
			f.Action = outputActionFailed
			f.Error = "submission has no ws:// output destination"
			return
		}
		name = stringField(ref.obj, "basename")
		if name == "" {
			name = filepath.Base(strings.TrimPrefix(f.Location, "file://"))
		}
		destDir = path.Clean(strings.TrimRight(ref.base, "/") + "/" + ref.subPath)
	} else {
		f.Action = outputActionFailed
		return // error already set by verify
	}
	if ref.base == "" || !wsPathUnder(path.Join(destDir, name), ref.base) {
		f.Action = outputActionFailed
		f.Error = fmt.Sprintf("workspace path %s is outside the submission's output destination %s", path.Join(destDir, name), ref.base)
		return
	}

	// Locate a local original: the File's own file:// path first, then any
	// task output with the same checksum+size. Every candidate is admitted by
	// checkCandidate before it is opened.
	key := originalKey(f.ExpectedChecksum, f.ExpectedSize)
	var candidates []string
	if strings.HasPrefix(f.Location, "file://") {
		candidates = append(candidates, strings.TrimPrefix(f.Location, "file://"))
	}
	if local, ok := index.paths[key]; ok && (len(candidates) == 0 || candidates[0] != local) {
		candidates = append(candidates, local)
	}
	original := ""
	reasons := append([]string(nil), index.skipped[key]...)
	if len(s.redeliverSourceDirs) == 0 && len(candidates) == 0 {
		reasons = append(reasons, s.checkCandidate("", 0))
	}
	for _, c := range candidates {
		if reason := s.checkCandidate(c, f.ExpectedSize); reason != "" {
			reasons = append(reasons, reason)
			continue
		}
		sum, n, err := sha1File(c)
		switch {
		case err != nil:
			reasons = append(reasons, err.Error())
		case sum != f.ExpectedChecksum || (f.ExpectedSize > 0 && n != f.ExpectedSize):
			reasons = append(reasons, fmt.Sprintf("%s does not match the recorded checksum/size", c))
		default:
			original = c
		}
		if original != "" {
			break
		}
	}
	if original == "" {
		f.Action = outputActionOriginalMissing
		f.Error = fmt.Sprintf("no usable local original with checksum %s and size %d found in task outputs", f.ExpectedChecksum, f.ExpectedSize)
		if len(reasons) > 0 {
			f.Error += " (" + strings.Join(reasons, "; ") + ")"
		}
		return
	}
	f.Source = original

	// StageOut names the object after the local file; the original was staged
	// by that same code, so the names agree unless something rewrote them.
	if filepath.Base(original) != name {
		f.Action = outputActionFailed
		f.Error = fmt.Sprintf("basename mismatch: original %q vs workspace object %q", filepath.Base(original), name)
		return
	}

	if dryRun {
		f.Action = outputActionWouldReupload
		return
	}

	stager := s.wsStager.WithToken(token)
	s.logger.Info("redeliver: re-uploading output",
		"submission_id", ref.sub.ID, "source", original, "dest", destDir+"/"+name)
	newLoc, err := stager.StageOut(ctx, original, ref.sub.ID, staging.StageOptions{
		Metadata: map[string]string{"destination": destDir},
	})
	if err != nil {
		f.Action = outputActionFailed
		f.Error = "re-upload: " + err.Error()
		return
	}

	// Re-verify what the workspace now serves.
	verifier := s.newWSVerifier(token)
	newPath, _ := wsPathOf(newLoc)
	urls, urlErr := verifier.downloadURLs(ctx, []string{newPath})
	prev := f.Location
	f.Location = newLoc
	s.verifyOne(ctx, verifier, f, urls, urlErr)
	if !f.OK {
		f.Action = outputActionFailed
		f.Error = "re-verify after upload: " + f.Error
		return
	}
	f.Action = outputActionReuploaded

	if newLoc != prev {
		ref.obj["location"] = newLoc
		changed[ref.sub.ID] = ref.sub
	}
}

// buildOutputReport assembles the response and tallies the summary.
func buildOutputReport(root *model.Submission, subs []*model.Submission, files []outputFileReport, dryRun bool) outputReport {
	report := outputReport{
		SubmissionID: root.ID,
		State:        string(root.State),
		OutputState:  root.OutputState,
		DryRun:       dryRun,
		Submissions:  make([]string, 0, len(subs)),
		Files:        files,
	}
	for _, sub := range subs {
		report.Submissions = append(report.Submissions, sub.ID)
	}
	sum := &report.Summary
	sum.Total = len(files)
	for i := range files {
		f := &files[i]
		f.ref = nil
		switch {
		case f.OK:
			sum.OK++
		case f.ActualChecksum != "":
			sum.Mismatched++
		default:
			sum.Errors++
		}
		switch f.Action {
		case outputActionReuploaded:
			sum.Reuploaded++
		case outputActionWouldReupload:
			sum.WouldReupload++
		case outputActionOriginalMissing:
			sum.OriginalMissing++
		case outputActionFailed:
			sum.Failed++
		}
	}
	return report
}

// wsPathOf extracts the workspace path from a ws:// URI or destination.
// ws:///user@bvbrc/home/x → /user@bvbrc/home/x
func wsPathOf(location string) (string, bool) {
	if !strings.HasPrefix(location, "ws://") {
		return "", false
	}
	p := strings.TrimPrefix(location, "ws://")
	p = "/" + strings.TrimLeft(p, "/")
	if p == "/" {
		return "", false
	}
	return p, true
}

// wsPathUnder reports whether workspace path p lies at or below base
// (segment-aware, both cleaned).
func wsPathUnder(p, base string) bool {
	p = path.Clean(p)
	base = path.Clean(base)
	return p == base || strings.HasPrefix(p, base+"/")
}

// localPathOf returns the local filesystem path of a task output File: its
// file:// location, or its bare "path" when no location was recorded.
func localPathOf(obj map[string]any) string {
	if loc := stringField(obj, "location"); loc != "" {
		if strings.HasPrefix(loc, "file://") {
			return strings.TrimPrefix(loc, "file://")
		}
		return ""
	}
	return stringField(obj, "path")
}

// sha1File returns the CWL-style checksum and size of a local file. Callers
// admit the path with checkCandidate first.
func sha1File(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha1.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha1$" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// int64Field reads a numeric field that may arrive as float64 (JSON round
// trip through the store), int, or int64.
func int64Field(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}
