package bvbrc

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// WorkspaceLsInput contains parameters for listing workspace contents.
type WorkspaceLsInput struct {
	// Paths is a list of workspace paths to list.
	Paths []string

	// Recursive lists contents recursively.
	Recursive bool

	// ExcludeDirectories excludes subdirectories from listing.
	ExcludeDirectories bool

	// ExcludeObjects excludes objects (files) from listing.
	ExcludeObjects bool
}

// WorkspaceLs lists the contents of workspace directories.
func (c *Client) WorkspaceLs(ctx context.Context, input WorkspaceLsInput) (map[string][]WorkspaceObject, error) {
	params := map[string]any{
		"paths": input.Paths,
	}
	if input.Recursive {
		params["recursive"] = true
	}
	if input.ExcludeDirectories {
		params["excludeDirectories"] = true
	}
	if input.ExcludeObjects {
		params["excludeObjects"] = true
	}

	resp, err := c.CallWorkspace(ctx, "Workspace.ls", params)
	if err != nil {
		return nil, err
	}

	// Result is [{path: [[obj_tuple], ...], ...}]
	var rawResult []map[string][][]any
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceLs", fmt.Errorf("unmarshaling result: %w", err))
	}

	result := make(map[string][]WorkspaceObject)
	if len(rawResult) == 0 {
		return result, nil
	}

	for path, tuples := range rawResult[0] {
		objects := make([]WorkspaceObject, 0, len(tuples))
		for _, tuple := range tuples {
			obj, err := parseWorkspaceObjectTuple(tuple)
			if err != nil {
				continue // Skip malformed entries
			}
			objects = append(objects, obj)
		}
		result[path] = objects
	}

	return result, nil
}

// WorkspaceGetInput contains parameters for getting workspace objects.
type WorkspaceGetInput struct {
	// Objects is a list of workspace paths to retrieve.
	Objects []string

	// MetadataOnly returns only metadata, not file content.
	MetadataOnly bool
}

// WorkspaceGet retrieves workspace objects.
func (c *Client) WorkspaceGet(ctx context.Context, input WorkspaceGetInput) ([]WorkspaceObject, error) {
	params := map[string]any{
		"objects": input.Objects,
	}
	if input.MetadataOnly {
		params["metadata_only"] = true
	}

	resp, err := c.CallWorkspace(ctx, "Workspace.get", params)
	if err != nil {
		return nil, err
	}

	// Workspace.spec: get(...) returns (list<tuple<ObjectMeta,ObjectData>>).
	// Wrapped in the JSON-RPC result array, that is [[[meta, data], ...]] — each
	// entry is a two-element pair, *not* the metadata tuple itself (issue #171).
	var rawResult [][][]any
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceGet", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(rawResult) == 0 || len(rawResult[0]) == 0 {
		return []WorkspaceObject{}, nil
	}

	objects := make([]WorkspaceObject, 0, len(rawResult[0]))
	for _, entry := range rawResult[0] {
		obj, err := parseWorkspaceGetEntry(entry)
		if err != nil {
			continue
		}
		objects = append(objects, obj)
	}

	return objects, nil
}

// parseWorkspaceGetEntry parses one [ObjectMeta, ObjectData] pair as returned by
// Workspace.get. The data half is optional: metadata_only requests and older
// deployments may return a bare metadata tuple, which is accepted as well.
func parseWorkspaceGetEntry(entry []any) (WorkspaceObject, error) {
	if len(entry) == 0 {
		return WorkspaceObject{}, fmt.Errorf("empty get entry")
	}

	meta, ok := entry[0].([]any)
	if !ok {
		// Not a [meta, data] pair — treat the entry itself as the metadata tuple.
		return parseWorkspaceObjectTuple(entry)
	}

	obj, err := parseWorkspaceObjectTuple(meta)
	if err != nil {
		return obj, err
	}
	if len(entry) > 1 {
		if s, ok := entry[1].(string); ok {
			obj.Data = s
		}
	}

	return obj, nil
}

// WorkspaceCreateInput contains parameters for creating workspace objects.
type WorkspaceCreateInput struct {
	// Path is the destination workspace path.
	Path string

	// Type is the object type (folder, contigs, reads, etc.).
	Type WorkspaceObjectType

	// Content is inline TEXT content (nil for folders or upload nodes). It is
	// sent as a JSON string, so it must be valid UTF-8; anything else is
	// rejected. Binary data must go through WorkspaceUploadReader.
	Content *string

	// Metadata is user-defined metadata.
	Metadata map[string]string

	// Overwrite allows overwriting existing objects.
	Overwrite bool

	// CreateUploadNodes creates a Shock upload node for large files.
	CreateUploadNodes bool
}

// WorkspaceCreate creates new workspace objects.
func (c *Client) WorkspaceCreate(ctx context.Context, input WorkspaceCreateInput) (*WorkspaceObject, error) {
	var content any
	if input.Content != nil {
		if !utf8.ValidString(*input.Content) {
			return nil, NewError("WorkspaceCreate",
				fmt.Sprintf("inline content for %s is not valid UTF-8; binary data must be uploaded with WorkspaceUploadReader", input.Path))
		}
		content = *input.Content
	}

	metadata := input.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}

	objSpec := []any{input.Path, string(input.Type), metadata, content}

	params := map[string]any{
		"objects": [][]any{objSpec},
	}
	if input.Overwrite {
		params["overwrite"] = true
	}
	if input.CreateUploadNodes {
		params["createUploadNodes"] = true
	}

	resp, err := c.CallWorkspace(ctx, "Workspace.create", params)
	if err != nil {
		return nil, err
	}

	var rawResult [][][]any
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceCreate", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(rawResult) == 0 || len(rawResult[0]) == 0 {
		return nil, NewError("WorkspaceCreate", "no object returned from server")
	}

	obj, err := parseWorkspaceObjectTuple(rawResult[0][0])
	if err != nil {
		return nil, WrapError("WorkspaceCreate", err)
	}

	return &obj, nil
}

// WorkspaceCreateFolder creates a new folder in the workspace.
func (c *Client) WorkspaceCreateFolder(ctx context.Context, path string) (*WorkspaceObject, error) {
	return c.WorkspaceCreate(ctx, WorkspaceCreateInput{
		Path: path,
		Type: WorkspaceTypeFolder,
	})
}

// WorkspaceUploadFile uploads file bytes to the workspace, binary-safe. It is
// a convenience wrapper over WorkspaceUploadReader for payloads that are
// already in memory (manifests, small inputs); stream anything large.
func (c *Client) WorkspaceUploadFile(ctx context.Context, wsPath string, data []byte, objType WorkspaceObjectType) (*WorkspaceObject, error) {
	return c.WorkspaceUploadReader(ctx, wsPath, path.Base(wsPath), bytes.NewReader(data), int64(len(data)), objType)
}

// shockNodeURLRe is the shape of a usable upload node URL. WorkspaceImpl.pm's
// _create_shock_node never checks its own Shock POST: when that fails the
// service still creates the object row and hands back "<shock>/node/" with an
// empty id, which must not be PUT to.
var shockNodeURLRe = regexp.MustCompile(`^https?://.+/node/[^/]+$`)

// placeholderDeleteTimeout bounds the best-effort cleanup of a failed upload.
const placeholderDeleteTimeout = 30 * time.Second

// WorkspaceUploadReader streams size bytes from r into the workspace object
// at wsPath, binary-safe, and verifies what the service recorded before it
// returns. It is single-attempt: callers own the retry loop and must supply a
// fresh reader for every attempt.
//
// It follows the service's own upload protocol, the one implemented by
// scripts/ws-create.pl --useshock and used by the BV-BRC clients:
//
//  1. Workspace.create the destination with createUploadNodes and overwrite
//     set. With overwrite the service deletes any existing object FIRST and
//     then allocates a new object with a new Shock node, whose URL comes back
//     in ObjectMeta slot [11]. Overwrite is therefore destructive-first: once
//     this call has started, the previous version of the object is gone.
//  2. PUT the bytes to that URL as multipart form field "upload" with a
//     non-empty filename, under an "Authorization: OAuth <token>" header. The
//     body is streamed with an exact Content-Length (no chunked encoding). A
//     node's file is immutable, so a PUT is never retried against the same
//     node; every attempt goes back to step 1.
//  3. Parse Shock's reply envelope and require that it stored exactly size
//     bytes (and, when it reports an md5, that it matches the bytes sent).
//  4. Workspace.update_auto_meta on the object, which makes the service fetch
//     the node's metadata and record the size; require that the returned
//     ObjectMeta carries size as well. The returned object is that refreshed
//     metadata.
//
// A zero-length payload is stored inline as an empty text object (Shock's
// handling of an empty multipart upload is unverified) and skips steps 2–4.
//
// On any failure after step 1 the freshly created placeholder is deleted on a
// best-effort basis so a half-written object does not masquerade as a
// delivered one, and the error is returned.
//
// Concurrent writers to the same path (scatter siblings sharing an output
// folder and a basename) are detected rather than raced: step 1 records the
// ObjectID the service allocated, step 4 requires the refreshed metadata to
// carry that same ID, and the cleanup deletes only an object that still has
// it. A writer that finds its object replaced returns an error and touches
// nothing, so the other writer's upload survives intact. The one placeholder
// that cannot be cleaned up is an object whose create reply carried no ID
// (nothing to guard the delete with); that case is logged and returned as an
// error without any delete.
//
// The Shock PUT is bounded by progress, not by total time: when
// Config.UploadStallTimeout passes without a byte read from the source or a
// reply arriving, the upload is abandoned with an "upload stalled" error.
//
// Do not be tempted back to Workspace.create's inline Content field for
// binary data: it is a JSON string, and encoding/json replaces every byte
// that is not valid UTF-8 with U+FFFD (3 bytes for 1), silently destroying
// every binary file that goes through it. That was issue #172.
func (c *Client) WorkspaceUploadReader(ctx context.Context, wsPath, filename string, r io.Reader, size int64, objType WorkspaceObjectType) (*WorkspaceObject, error) {
	const op = "WorkspaceUploadReader"

	// The multipart part must carry a real filename: the service's
	// _update_shock_node treats an empty file name as "not uploaded yet" and
	// never records the size, so the upload is accepted but reported as 0 bytes
	// forever. path.Base("") is "." — catch that too.
	switch filename {
	case "", ".", "..", "/":
		return nil, NewError(op, fmt.Sprintf("invalid upload filename %q for %s", filename, wsPath))
	}
	if strings.ContainsAny(filename, "/\\") {
		return nil, NewError(op, fmt.Sprintf("upload filename %q must be a bare name", filename))
	}
	if size < 0 {
		return nil, NewError(op, fmt.Sprintf("negative size %d for %s", size, wsPath))
	}

	if size == 0 {
		empty := ""
		obj, err := c.WorkspaceCreate(ctx, WorkspaceCreateInput{
			Path:      wsPath,
			Type:      objType,
			Content:   &empty,
			Overwrite: true,
		})
		if err != nil {
			return nil, err
		}
		return obj, nil
	}

	obj, err := c.WorkspaceCreate(ctx, WorkspaceCreateInput{
		Path:              wsPath,
		Type:              objType,
		Overwrite:         true,
		CreateUploadNodes: true,
	})
	if err != nil {
		return nil, err
	}
	if obj == nil || obj.ID == "" {
		// The object was created but its reply is unusable: without the ID
		// there is nothing to guard a delete with, and an unguarded delete
		// could take out another writer's object. Leave it and say so.
		c.logger.Warn("workspace create reply carried no object ID; placeholder may remain", "path", wsPath)
		return nil, NewError(op, fmt.Sprintf("create reply for %s carried no object ID; placeholder may remain at %s", wsPath, wsPath))
	}
	createdID := obj.ID

	fail := func(err error) (*WorkspaceObject, error) {
		c.deletePlaceholder(ctx, wsPath, createdID)
		return nil, WrapError(op, err)
	}

	if obj.ShockURL == "" {
		// Never fall back to the inline path: for a binary payload that would
		// silently store corrupt bytes. Fail loudly instead.
		return fail(fmt.Errorf("no Shock upload URL returned for %s (ObjectMeta slot 11 empty)", wsPath))
	}
	if !shockNodeURLRe.MatchString(obj.ShockURL) {
		return fail(fmt.Errorf("malformed Shock upload URL %q for %s (the service's node allocation failed)", obj.ShockURL, wsPath))
	}

	sentMD5, err := c.shockPut(ctx, obj.ShockURL, filename, r, size)
	if err != nil {
		return fail(err)
	}

	meta, err := c.WorkspaceUpdateAutoMeta(ctx, wsPath)
	if err != nil {
		return fail(fmt.Errorf("refreshing object metadata after upload: %w", err))
	}
	// The identity check comes before the size check: a replacement object
	// naturally has some other size, and that must not be reported (or
	// cleaned up) as if it were ours.
	if meta.ID != createdID {
		return nil, NewError(op, fmt.Sprintf("object at %s was replaced concurrently by another writer (created %s, now %s)", wsPath, createdID, meta.ID))
	}
	if meta.Size != size {
		return fail(fmt.Errorf("Workspace recorded %d bytes for %s, expected %d", meta.Size, wsPath, size))
	}

	c.logger.Debug("workspace upload verified", "path", wsPath, "size", size, "md5", sentMD5)
	return meta, nil
}

// deletePlaceholder removes the object left behind by a failed upload, but
// only while it is still the object this upload created: it reads the
// object's metadata first and deletes nothing unless the ID matches
// createdID. If the object cannot be read (already gone, service error) the
// delete is skipped rather than risked. It is best-effort and detached from
// the caller's cancellation so a cancelled upload still gets cleaned up.
func (c *Client) deletePlaceholder(ctx context.Context, wsPath, createdID string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), placeholderDeleteTimeout)
	defer cancel()

	objs, err := c.WorkspaceGet(dctx, WorkspaceGetInput{Objects: []string{wsPath}, MetadataOnly: true})
	if err != nil {
		c.logger.Warn("could not read workspace placeholder after failed upload; not deleting", "path", wsPath, "error", err)
		return
	}
	if len(objs) == 0 {
		c.logger.Warn("workspace placeholder not found after failed upload; nothing to delete", "path", wsPath)
		return
	}
	if objs[0].ID != createdID {
		c.logger.Warn("workspace object was replaced by another writer during the failed upload; not deleting",
			"path", wsPath, "created_id", createdID, "current_id", objs[0].ID)
		return
	}

	if err := c.WorkspaceDelete(dctx, WorkspaceDeleteInput{Objects: []string{wsPath}}); err != nil {
		c.logger.Warn("could not delete workspace placeholder after failed upload", "path", wsPath, "error", err)
	}
}

// shockPutReply is the envelope Shock returns for a node file PUT. The error
// member is null on success and a list of strings otherwise; older servers
// emitted a single string.
type shockPutReply struct {
	Status int         `json:"status"`
	Error  shockErrors `json:"error"`
	Data   struct {
		ID   string `json:"id"`
		File struct {
			Name     string            `json:"name"`
			Size     int64             `json:"size"`
			Checksum map[string]string `json:"checksum"`
		} `json:"file"`
	} `json:"data"`
}

type shockErrors []string

func (e *shockErrors) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*e = nil
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*e = list
		return nil
	}
	var single string
	if err := json.Unmarshal(b, &single); err != nil {
		return err
	}
	*e = []string{single}
	return nil
}

// countingWriter tallies bytes and feeds them to an md5 hash.
type countingWriter struct {
	n    int64
	hash hash.Hash
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return w.hash.Write(p)
}

// errUploadStalled is the cancellation cause set by the progress watchdog.
var errUploadStalled = errors.New("upload stalled")

// progressReader wraps the upload source: every Read pushes the stall
// watchdog out by another interval, and once the request context is done
// (watchdog fired or caller cancelled) it stops handing out bytes so the
// transport's body-writing goroutine does not sit in a blocked source.
type progressReader struct {
	r        io.Reader
	ctx      context.Context
	watchdog *time.Timer
	interval time.Duration

	mu      sync.Mutex
	stopped bool
}

func (p *progressReader) Read(b []byte) (int, error) {
	if err := p.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := p.r.Read(b)
	p.mu.Lock()
	if !p.stopped {
		p.watchdog.Reset(p.interval)
	}
	p.mu.Unlock()
	return n, err
}

// stop halts the watchdog for good; later Reads no longer re-arm it.
func (p *progressReader) stop() {
	p.mu.Lock()
	p.stopped = true
	p.watchdog.Stop()
	p.mu.Unlock()
}

func (c *Client) uploadStallTimeout() time.Duration {
	if c.config.UploadStallTimeout > 0 {
		return c.config.UploadStallTimeout
	}
	return DefaultUploadStallTimeout
}

// shockPut streams size bytes from r to a Shock node URL as multipart form
// field "upload" and verifies the reply. It returns the md5 of the bytes sent.
//
// The body is io.MultiReader(head, TeeReader(r), tail) with the multipart
// head and tail built ahead of time, so the request carries an exact
// Content-Length instead of chunked transfer encoding (which the nginx in
// front of Shock has rejected in the past). net/http fails the request if the
// reader yields more or fewer bytes than declared, so a file that changed
// size underneath us cannot be uploaded truncated or padded.
//
// A progress watchdog (Config.UploadStallTimeout) cancels the request when
// no byte has been read from r for that long — a wedged Shock that stops
// draining the socket, or one that never answers once the body is sent —
// so the upload cannot hang indefinitely.
func (c *Client) shockPut(ctx context.Context, shockURL, filename string, r io.Reader, size int64) (string, error) {
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	if _, err := mw.CreateFormFile("upload", filename); err != nil {
		return "", fmt.Errorf("building multipart header: %w", err)
	}
	tail := []byte("\r\n--" + mw.Boundary() + "--\r\n")

	stall := c.uploadStallTimeout()
	uctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watchdog := time.AfterFunc(stall, func() { cancel(errUploadStalled) })
	source := &progressReader{r: r, ctx: uctx, watchdog: watchdog, interval: stall}
	defer source.stop()

	counter := &countingWriter{hash: md5.New()}
	body := io.MultiReader(&head, io.TeeReader(source, counter), bytes.NewReader(tail))

	req, err := http.NewRequestWithContext(uctx, http.MethodPut, shockURL, body)
	if err != nil {
		return "", fmt.Errorf("creating Shock request: %w", err)
	}
	req.ContentLength = int64(head.Len()) + size + int64(len(tail))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.config.Token != "" {
		req.Header.Set("Authorization", "OAuth "+c.config.Token)
	}

	resp, err := c.uploadClient.Do(req)
	if err != nil {
		// Diagnose in order of certainty. A stall or a caller cancellation is
		// a known cause and usually leaves the source part-read, so it must
		// be reported before the byte count is interpreted; only an
		// uncancelled request with a short or long source points at the
		// file having changed underneath the upload.
		switch {
		case errors.Is(context.Cause(uctx), errUploadStalled):
			return "", fmt.Errorf("upload stalled: no progress for %s (sent %d of %d bytes): %w", stall, counter.n, size, errUploadStalled)
		case uctx.Err() != nil:
			return "", fmt.Errorf("Shock upload request: %w", err)
		case counter.n != size:
			return "", fmt.Errorf("read %d bytes from source, expected %d (file changed during upload?): %w", counter.n, size, err)
		default:
			return "", fmt.Errorf("Shock upload request: %w", err)
		}
	}
	defer resp.Body.Close()

	if counter.n != size {
		return "", fmt.Errorf("read %d bytes from source, expected %d (file changed during upload?)", counter.n, size)
	}

	// The reply is read under the same watchdog: re-arm it once for the
	// (small) envelope so a Shock that sends headers and then stops cannot
	// hang the upload either.
	watchdog.Reset(stall)
	replyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if errors.Is(context.Cause(uctx), errUploadStalled) {
			return "", fmt.Errorf("upload stalled: no progress for %s while reading the Shock reply: %w", stall, errUploadStalled)
		}
		return "", fmt.Errorf("reading Shock reply: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", &HTTPError{StatusCode: resp.StatusCode, Body: string(replyBytes)}
	}

	var reply shockPutReply
	if err := json.Unmarshal(replyBytes, &reply); err != nil {
		return "", fmt.Errorf("parsing Shock reply: %w (body: %.200q)", err, replyBytes)
	}
	if len(reply.Error) > 0 {
		return "", fmt.Errorf("Shock reported an error: %s", strings.Join(reply.Error, "; "))
	}
	if reply.Status >= 400 {
		return "", fmt.Errorf("Shock reported status %d", reply.Status)
	}
	if reply.Data.File.Size != size {
		return "", fmt.Errorf("Shock stored %d bytes, expected %d", reply.Data.File.Size, size)
	}

	sentMD5 := hex.EncodeToString(counter.hash.Sum(nil))
	if got := reply.Data.File.Checksum["md5"]; got != "" && !strings.EqualFold(got, sentMD5) {
		return "", fmt.Errorf("Shock md5 %s does not match sent %s", got, sentMD5)
	}

	return sentMD5, nil
}

// WorkspaceUpdateAutoMeta asks the service to refresh the automatic metadata
// of the object at wsPath and returns the refreshed ObjectMeta. For a
// Shock-backed object the service fetches the node's metadata and records the
// stored file size, so the returned Size is authoritative for what the
// Workspace now believes the object holds. The path must name an object, not
// a folder.
func (c *Client) WorkspaceUpdateAutoMeta(ctx context.Context, wsPath string) (*WorkspaceObject, error) {
	const op = "WorkspaceUpdateAutoMeta"

	resp, err := c.CallWorkspace(ctx, "Workspace.update_auto_meta", map[string]any{
		"objects": []string{wsPath},
	})
	if err != nil {
		return nil, err
	}

	// Workspace.spec: update_auto_meta returns list<ObjectMeta>.
	var rawResult [][][]any
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError(op, fmt.Errorf("unmarshaling result: %w", err))
	}
	if len(rawResult) == 0 || len(rawResult[0]) == 0 {
		return nil, NewError(op, "no object returned from server")
	}

	obj, err := parseWorkspaceObjectTuple(rawResult[0][0])
	if err != nil {
		return nil, WrapError(op, err)
	}

	return &obj, nil
}

// WorkspaceDeleteInput contains parameters for deleting workspace objects.
type WorkspaceDeleteInput struct {
	// Objects is a list of paths to delete.
	Objects []string

	// Force deletes even if the object is not empty.
	Force bool

	// DeleteDirectories allows deletion of directories.
	DeleteDirectories bool
}

// WorkspaceDelete deletes workspace objects.
func (c *Client) WorkspaceDelete(ctx context.Context, input WorkspaceDeleteInput) error {
	params := map[string]any{
		"objects": input.Objects,
	}
	if input.Force {
		params["force"] = true
	}
	if input.DeleteDirectories {
		params["deleteDirectories"] = true
	}

	_, err := c.CallWorkspace(ctx, "Workspace.delete", params)
	return err
}

// WorkspaceCopyInput contains parameters for copying workspace objects.
type WorkspaceCopyInput struct {
	// Objects is a list of [source, destination] pairs.
	Objects [][2]string

	// Overwrite allows overwriting existing objects.
	Overwrite bool

	// Recursive copies directories recursively.
	Recursive bool
}

// WorkspaceCopy copies workspace objects.
func (c *Client) WorkspaceCopy(ctx context.Context, input WorkspaceCopyInput) ([]WorkspaceObject, error) {
	// Convert to array of arrays
	objects := make([][]string, len(input.Objects))
	for i, pair := range input.Objects {
		objects[i] = []string{pair[0], pair[1]}
	}

	params := map[string]any{
		"objects": objects,
	}
	if input.Overwrite {
		params["overwrite"] = true
	}
	if input.Recursive {
		params["recursive"] = true
	}

	resp, err := c.CallWorkspace(ctx, "Workspace.copy", params)
	if err != nil {
		return nil, err
	}

	var rawResult [][][]any
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceCopy", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(rawResult) == 0 {
		return []WorkspaceObject{}, nil
	}

	objectsResult := make([]WorkspaceObject, 0, len(rawResult[0]))
	for _, tuple := range rawResult[0] {
		obj, err := parseWorkspaceObjectTuple(tuple)
		if err != nil {
			continue
		}
		objectsResult = append(objectsResult, obj)
	}

	return objectsResult, nil
}

// WorkspaceMoveInput contains parameters for moving workspace objects.
type WorkspaceMoveInput struct {
	// Objects is a list of [source, destination] pairs.
	Objects [][2]string

	// Overwrite allows overwriting existing objects.
	Overwrite bool
}

// WorkspaceMove moves or renames workspace objects.
func (c *Client) WorkspaceMove(ctx context.Context, input WorkspaceMoveInput) ([]WorkspaceObject, error) {
	objects := make([][]string, len(input.Objects))
	for i, pair := range input.Objects {
		objects[i] = []string{pair[0], pair[1]}
	}

	params := map[string]any{
		"objects": objects,
	}
	if input.Overwrite {
		params["overwrite"] = true
	}

	resp, err := c.CallWorkspace(ctx, "Workspace.move", params)
	if err != nil {
		return nil, err
	}

	var rawResult [][][]any
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceMove", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(rawResult) == 0 {
		return []WorkspaceObject{}, nil
	}

	objectsResult := make([]WorkspaceObject, 0, len(rawResult[0]))
	for _, tuple := range rawResult[0] {
		obj, err := parseWorkspaceObjectTuple(tuple)
		if err != nil {
			continue
		}
		objectsResult = append(objectsResult, obj)
	}

	return objectsResult, nil
}

// WorkspaceSetPermissionsInput contains parameters for setting permissions.
type WorkspaceSetPermissionsInput struct {
	// Path is the workspace path to set permissions on.
	Path string

	// Permissions is a list of [username, permission] pairs.
	// Permission can be "r" (read), "w" (write), "o" (owner), "n" (none).
	Permissions []WorkspacePermission
}

// WorkspaceSetPermissions sets sharing permissions on workspace objects.
func (c *Client) WorkspaceSetPermissions(ctx context.Context, input WorkspaceSetPermissionsInput) error {
	perms := make([][]string, len(input.Permissions))
	for i, p := range input.Permissions {
		perms[i] = []string{p.Username, p.Permission}
	}

	params := map[string]any{
		"path":        input.Path,
		"permissions": perms,
	}

	_, err := c.CallWorkspace(ctx, "Workspace.set_permissions", params)
	return err
}

// WorkspaceListPermissions lists permissions on workspace objects.
func (c *Client) WorkspaceListPermissions(ctx context.Context, paths []string) (map[string][]WorkspacePermission, error) {
	params := map[string]any{
		"objects": paths,
	}

	resp, err := c.CallWorkspace(ctx, "Workspace.list_permissions", params)
	if err != nil {
		return nil, err
	}

	// Result format varies; try to parse
	var rawResult []map[string][][]string
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceListPermissions", fmt.Errorf("unmarshaling result: %w", err))
	}

	result := make(map[string][]WorkspacePermission)
	if len(rawResult) == 0 {
		return result, nil
	}

	for path, perms := range rawResult[0] {
		permissions := make([]WorkspacePermission, 0, len(perms))
		for _, p := range perms {
			if len(p) >= 2 {
				permissions = append(permissions, WorkspacePermission{
					Username:   p[0],
					Permission: p[1],
				})
			}
		}
		result[path] = permissions
	}

	return result, nil
}

// WorkspaceGetDownloadURL gets download URLs for workspace objects.
func (c *Client) WorkspaceGetDownloadURL(ctx context.Context, paths []string) (map[string]string, error) {
	params := map[string]any{
		"objects": paths,
	}

	resp, err := c.CallWorkspace(ctx, "Workspace.get_download_url", params)
	if err != nil {
		return nil, err
	}

	// Workspace.spec: get_download_url returns list<string>, one entry per
	// requested path in input order, wrapped in the JSON-RPC result array:
	// [[url1, url2, ...]]. Folders and missing objects come back as JSON null,
	// which maps to "" — callers treat "" as "no URL".
	var rawResult [][]*string
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceGetDownloadURL", fmt.Errorf("unmarshaling result: %w", err))
	}

	result := make(map[string]string, len(paths))
	if len(rawResult) == 0 {
		return result, nil
	}
	for i, url := range rawResult[0] {
		if i >= len(paths) {
			break
		}
		if url != nil {
			result[paths[i]] = *url
		} else {
			result[paths[i]] = ""
		}
	}

	return result, nil
}

// parseWorkspaceObjectTuple parses a Workspace ObjectMeta tuple into a WorkspaceObject.
//
// The layout is the ObjectMeta typedef in the Workspace module's Workspace.spec:
//
//	[ObjectName, ObjectType, FullObjectPath, creation_time, ObjectID, object_owner,
//	 ObjectSize, UserMetadata, AutoMetadata, user_permission, global_permission,
//	 shockurl, error]
//
// The spec declares 13 slots but WorkspaceImpl.pm's _generate_object_meta returns
// the first 12 (no error slot), so every slot past 8 is optional here.
//
// Note in particular that shockurl is slot [11], not [10]: [9] and [10] are the
// user and global permission strings ("o", "w", "r", "n"). Reading [10] as a Shock
// reference yields a permission letter — see issue #171.
func parseWorkspaceObjectTuple(tuple []any) (WorkspaceObject, error) {
	obj := WorkspaceObject{}

	if len(tuple) < 9 {
		return obj, fmt.Errorf("tuple too short: %d elements", len(tuple))
	}

	// Index 0: ObjectName
	if s, ok := tuple[0].(string); ok {
		obj.Name = s
	}

	// Index 1: ObjectType
	if s, ok := tuple[1].(string); ok {
		obj.Type = WorkspaceObjectType(s)
	}

	// Index 2: FullObjectPath — emitted as the containing directory with a
	// trailing slash; the service's own clients form the object path as
	// tuple[2] . tuple[0] (FileListing.pm, WorkspaceClientExt.pm).
	if s, ok := tuple[2].(string); ok {
		obj.Path = joinWorkspacePath(s, obj.Name)
	}

	// Index 3: creation_time
	if s, ok := tuple[3].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			obj.CreationTime = t
		}
	}

	// Index 4: ObjectID
	if s, ok := tuple[4].(string); ok {
		obj.ID = s
	}

	// Index 5: object_owner
	if s, ok := tuple[5].(string); ok {
		obj.Owner = s
	}

	// Index 6: ObjectSize
	switch v := tuple[6].(type) {
	case float64:
		obj.Size = int64(v)
	case int64:
		obj.Size = v
	case int:
		obj.Size = int64(v)
	}

	// Index 7: UserMetadata
	if m, ok := tuple[7].(map[string]any); ok {
		obj.UserMetadata = make(map[string]string)
		for k, v := range m {
			if s, ok := v.(string); ok {
				obj.UserMetadata[k] = s
			}
		}
	}

	// Index 8: AutoMetadata
	if m, ok := tuple[8].(map[string]any); ok {
		obj.AutoMetadata = make(map[string]string)
		for k, v := range m {
			if s, ok := v.(string); ok {
				obj.AutoMetadata[k] = s
			}
		}
	}

	// Index 9: user_permission
	if len(tuple) > 9 {
		if s, ok := tuple[9].(string); ok {
			obj.UserPermission = s
		}
	}

	// Index 10: global_permission
	if len(tuple) > 10 {
		if s, ok := tuple[10].(string); ok {
			obj.GlobalPermission = s
		}
	}

	// Index 11: shockurl
	if len(tuple) > 11 {
		if s, ok := tuple[11].(string); ok {
			obj.ShockURL = s
		}
	}

	// Index 12: error
	if len(tuple) > 12 {
		if s, ok := tuple[12].(string); ok {
			obj.Error = s
		}
	}

	return obj, nil
}

// joinWorkspacePath joins the directory slot of an ObjectMeta tuple with the
// object name. The service emits the directory with a trailing slash; tolerate
// its absence.
func joinWorkspacePath(dir, name string) string {
	switch {
	case name == "":
		return dir
	case dir == "":
		return name
	case strings.HasSuffix(dir, "/"):
		return dir + name
	default:
		return dir + "/" + name
	}
}
