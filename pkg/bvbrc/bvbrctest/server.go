// Package bvbrctest provides an httptest-backed fake of the BV-BRC Workspace
// JSON-RPC service together with the Shock node endpoint it hands out, so the
// upload protocol can be exercised end to end without a live deployment.
//
// The fake models the protocol facts that matter for correctness (see
// docs/BVBRC-API.md and WorkspaceImpl.pm):
//
//   - Workspace.create with createUploadNodes mints a fresh Shock node on every
//     call and, with overwrite, replaces any existing object (destructive-first);
//   - ObjectMeta tuples are the 12-slot shape _generate_object_meta emits, with
//     the Shock URL in slot [11];
//   - a Shock node accepts exactly one file PUT (immutable afterwards);
//   - Workspace.update_auto_meta reports the size Shock recorded for the node;
//   - Workspace.get returns the Shock URL as the data half for Shock-backed
//     objects and the inline content otherwise;
//   - Workspace.get_download_url returns one flat list in input order with
//     null for folders and missing objects.
//
// It must not import pkg/bvbrc (pkg/bvbrc's own tests use it), so requests and
// replies are decoded and encoded by hand.
package bvbrctest

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Object is one stored workspace object.
type Object struct {
	Path     string
	ID       string // ObjectID (slot [4]); assigned by the fake when empty
	Type     string
	Content  string // inline content (text objects)
	NodeID   string // backing Shock node, empty for inline objects
	Size     int64  // size as the Workspace has recorded it
	Metadata map[string]any
}

// Call is one recorded JSON-RPC call.
type Call struct {
	Method string
	Params json.RawMessage
}

// ShockPut records one Shock node PUT exactly as the server saw it.
type ShockPut struct {
	NodeID           string
	Method           string
	Authorization    string
	ContentLength    int64    // the declared Content-Length (-1 when chunked)
	RawLength        int64    // request body bytes actually received
	TransferEncoding []string // non-empty means chunked
	FormField        string
	Filename         string
	Body             []byte // bytes of the multipart part, not the whole request
}

// countingReader tallies the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Node is one Shock node.
type Node struct {
	ID       string
	Filename string
	Body     []byte
	HasFile  bool
}

// Server is the fake. Configure the exported hook fields before issuing
// requests; every recorded field is guarded by the mutex and read through the
// accessor methods.
type Server struct {
	srv *httptest.Server

	// ShockReply, when set, chooses the HTTP status and body of the Shock PUT
	// response; returning status 0 selects the default reply, a well-formed
	// Shock envelope reporting the size and md5 of the bytes received.
	ShockReply func(put ShockPut) (status int, body string)

	// ShockURL, when set, overrides the URL placed in ObjectMeta slot [11] for
	// a freshly minted node (for example to simulate the "<shock>/node/" the
	// service emits when its own Shock POST failed).
	ShockURL func(nodeID string) string

	// AutoMetaSize, when set, overrides the size update_auto_meta reports for
	// the object at path; recorded is the size Shock stored for its node.
	AutoMetaSize func(path string, recorded int64) int64

	// Intercept, when set, runs before every JSON-RPC call is dispatched,
	// after it has been recorded and with the fake unlocked, so it may use
	// Put and the accessors to change the stored state between two steps of
	// a protocol (for example to stand in for a concurrent writer). Returning
	// a non-zero status short-circuits the call with that HTTP status and
	// body, which injects a transport-level failure such as a bare 500 that
	// the client treats as retryable.
	Intercept func(method string, params json.RawMessage) (status int, body string)

	// RPCError, when set, is consulted after Intercept and before the method
	// is dispatched; a non-zero code makes the fake answer with that JSON-RPC
	// error (for example -32401 for a permission failure) instead.
	RPCError func(method string, params json.RawMessage) (code int, message string)

	// LsReply, when set, replaces the Workspace.ls result for the requested
	// paths whenever it returns ok — for instance an empty map, which the real
	// service can emit for a directory it knows but has nothing to list.
	LsReply func(paths []string) (result map[string][][]any, ok bool)

	// HoldShockBody, when set and returning a non-nil channel for a node,
	// makes the Shock PUT handler accept the request headers and then never
	// read the body: it blocks until the channel is closed, stores nothing,
	// and replies 500. It simulates a Shock that stops draining the socket.
	// Held handlers that have returned are counted by HeldReturned.
	HoldShockBody func(nodeID string) <-chan struct{}

	mu           sync.Mutex
	objects      map[string]*Object
	nodes        map[string]*Node
	calls        []Call
	puts         []ShockPut
	seq          int
	objSeq       int
	heldReturned int
}

// New starts a fake and registers its shutdown with t.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		objects: map[string]*Object{},
		nodes:   map[string]*Node{},
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

// WorkspaceURL is the JSON-RPC endpoint to configure clients with.
func (s *Server) WorkspaceURL() string { return s.srv.URL + "/services/Workspace" }

// BaseURL is the root of the fake's HTTP server.
func (s *Server) BaseURL() string { return s.srv.URL }

// Calls returns every JSON-RPC call received so far, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// CallsTo returns the calls made to one JSON-RPC method.
func (s *Server) CallsTo(method string) []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Puts returns every Shock PUT received so far, in order.
func (s *Server) Puts() []ShockPut {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ShockPut(nil), s.puts...)
}

// LastPut returns the most recent Shock PUT, or false if there was none.
func (s *Server) LastPut() (ShockPut, bool) {
	puts := s.Puts()
	if len(puts) == 0 {
		return ShockPut{}, false
	}
	return puts[len(puts)-1], true
}

// Object returns the stored object at path, or nil.
func (s *Server) Object(path string) *Object {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[path]
	if !ok {
		return nil
	}
	cp := *o
	return &cp
}

// Bytes returns the bytes Shock holds for the object at path (nil if the
// object is missing, inline, or was never uploaded).
func (s *Server) Bytes(path string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[path]
	if !ok || o.NodeID == "" {
		return nil
	}
	n, ok := s.nodes[o.NodeID]
	if !ok {
		return nil
	}
	return append([]byte(nil), n.Body...)
}

// Put stores an object directly, bypassing the protocol (test setup, or a
// stand-in for another writer). An empty ID gets a fresh one.
func (s *Server) Put(obj Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := obj
	if cp.ID == "" {
		cp.ID = s.nextObjectIDLocked()
	}
	s.objects[obj.Path] = &cp
}

// HeldReturned reports how many Shock handlers parked by HoldShockBody have
// returned, which proves the client side gave up on the request.
func (s *Server) HeldReturned() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldReturned
}

// nextObjectIDLocked mints a distinct ObjectID. Callers hold s.mu.
func (s *Server) nextObjectIDLocked() string {
	s.objSeq++
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", s.objSeq)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/shock_api/node/"):
		s.handleShock(w, r)
	case strings.HasPrefix(r.URL.Path, "/download/"):
		s.handleDownload(w, r)
	default:
		s.handleRPC(w, r)
	}
}

// ---- JSON-RPC ------------------------------------------------------------

type rpcError struct {
	Name    string `json:"name"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeResult(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "1", "version": "1.1", "result": []any{result},
	})
}

func writeError(w http.ResponseWriter, msg string) {
	writeErrorCode(w, -32603, msg)
}

func writeErrorCode(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "1", "version": "1.1",
		"error": rpcError{Name: "JSONRPCError", Code: code, Message: msg},
	})
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var params json.RawMessage
	if len(req.Params) > 0 {
		params = req.Params[0]
	}

	s.mu.Lock()
	s.calls = append(s.calls, Call{Method: req.Method, Params: params})
	s.mu.Unlock()

	if s.Intercept != nil {
		if status, body := s.Intercept(req.Method, params); status != 0 {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
	}

	if s.RPCError != nil {
		if code, msg := s.RPCError(req.Method, params); code != 0 {
			writeErrorCode(w, code, msg)
			return
		}
	}

	switch req.Method {
	case "Workspace.create":
		s.rpcCreate(w, params)
	case "Workspace.get":
		s.rpcGet(w, params)
	case "Workspace.ls":
		s.rpcLs(w, params)
	case "Workspace.delete":
		s.rpcDelete(w, params)
	case "Workspace.update_auto_meta":
		s.rpcUpdateAutoMeta(w, params)
	case "Workspace.get_download_url":
		s.rpcGetDownloadURL(w, params)
	default:
		writeError(w, "unsupported method "+req.Method)
	}
}

func (s *Server) rpcCreate(w http.ResponseWriter, raw json.RawMessage) {
	var p struct {
		Objects           [][]any `json:"objects"`
		Overwrite         bool    `json:"overwrite"`
		CreateUploadNodes bool    `json:"createUploadNodes"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		writeError(w, "bad params: "+err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var metas [][]any
	for _, spec := range p.Objects {
		if len(spec) < 2 {
			writeError(w, "object spec too short")
			return
		}
		path, _ := spec[0].(string)
		typ, _ := spec[1].(string)
		var meta map[string]any
		if len(spec) > 2 {
			meta, _ = spec[2].(map[string]any)
		}
		var content any
		if len(spec) > 3 {
			content = spec[3]
		}

		if _, exists := s.objects[path]; exists && !p.Overwrite {
			writeError(w, "Object "+path+" already exists!")
			return
		}

		// Overwrite is destructive-first: the previous object (and its node
		// binding) is gone before the new one is created.
		delete(s.objects, path)

		obj := &Object{Path: path, ID: s.nextObjectIDLocked(), Type: typ, Metadata: meta}
		if c, ok := content.(string); ok {
			obj.Content = c
			obj.Size = int64(len(c))
		}
		shockURL := ""
		if p.CreateUploadNodes {
			s.seq++
			id := fmt.Sprintf("node-%03d", s.seq)
			s.nodes[id] = &Node{ID: id}
			obj.NodeID = id
			shockURL = s.srv.URL + "/shock_api/node/" + id
			if s.ShockURL != nil {
				shockURL = s.ShockURL(id)
			}
		}
		s.objects[path] = obj
		metas = append(metas, s.metaLocked(obj, shockURL, p.CreateUploadNodes))
	}

	writeResult(w, metas)
}

func (s *Server) rpcGet(w http.ResponseWriter, raw json.RawMessage) {
	var p struct {
		Objects      []string `json:"objects"`
		MetadataOnly bool     `json:"metadata_only"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		writeError(w, "bad params: "+err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var entries [][]any
	for _, path := range p.Objects {
		obj, ok := s.objects[path]
		if !ok {
			writeError(w, "Object not found: "+path)
			return
		}
		data := ""
		if !p.MetadataOnly {
			if obj.NodeID != "" {
				data = s.srv.URL + "/shock_api/node/" + obj.NodeID
			} else {
				data = obj.Content
			}
		}
		entries = append(entries, []any{s.metaLocked(obj, "", false), data})
	}

	writeResult(w, entries)
}

func (s *Server) rpcLs(w http.ResponseWriter, raw json.RawMessage) {
	var p struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		writeError(w, "bad params: "+err.Error())
		return
	}

	if s.LsReply != nil {
		if result, ok := s.LsReply(p.Paths); ok {
			writeResult(w, result)
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result := map[string][][]any{}
	for _, dir := range p.Paths {
		prefix := strings.TrimRight(dir, "/") + "/"
		metas := [][]any{}
		for path, obj := range s.objects {
			if strings.HasPrefix(path, prefix) && !strings.Contains(strings.TrimPrefix(path, prefix), "/") {
				metas = append(metas, s.metaLocked(obj, "", false))
			}
		}
		result[dir] = metas
	}

	writeResult(w, result)
}

func (s *Server) rpcDelete(w http.ResponseWriter, raw json.RawMessage) {
	var p struct {
		Objects []string `json:"objects"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		writeError(w, "bad params: "+err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var metas [][]any
	for _, target := range p.Objects {
		for path, obj := range s.objects {
			if path == target || strings.HasPrefix(path, strings.TrimRight(target, "/")+"/") {
				metas = append(metas, s.metaLocked(obj, "", false))
				delete(s.objects, path)
			}
		}
	}

	writeResult(w, metas)
}

func (s *Server) rpcUpdateAutoMeta(w http.ResponseWriter, raw json.RawMessage) {
	var p struct {
		Objects []string `json:"objects"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		writeError(w, "bad params: "+err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var metas [][]any
	for _, path := range p.Objects {
		obj, ok := s.objects[path]
		if !ok {
			writeError(w, "Object not found: "+path)
			return
		}
		if obj.Type == "folder" {
			writeError(w, "Path does not point to an object!")
			return
		}
		// Mirrors _update_shock_node: the size is only recorded once the node
		// carries a file with a name.
		if n, ok := s.nodes[obj.NodeID]; ok && n.HasFile && n.Filename != "" {
			obj.Size = int64(len(n.Body))
		}
		if s.AutoMetaSize != nil {
			obj.Size = s.AutoMetaSize(path, obj.Size)
		}
		metas = append(metas, s.metaLocked(obj, "", false))
	}

	writeResult(w, metas)
}

func (s *Server) rpcGetDownloadURL(w http.ResponseWriter, raw json.RawMessage) {
	var p struct {
		Objects []string `json:"objects"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		writeError(w, "bad params: "+err.Error())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// One flat list in input order; null for folders and missing objects.
	urls := make([]any, 0, len(p.Objects))
	for _, path := range p.Objects {
		obj, ok := s.objects[path]
		if !ok || obj.Type == "folder" || obj.NodeID == "" {
			urls = append(urls, nil)
			continue
		}
		urls = append(urls, s.srv.URL+"/download/"+obj.NodeID)
	}

	writeResult(w, urls)
}

// metaLocked builds a 12-slot ObjectMeta tuple, shaped exactly like
// WorkspaceImpl.pm's _generate_object_meta: it stops at shockurl and omits the
// error slot. With explicitURL set, shockURL is emitted verbatim (the create
// reply, where the ShockURL hook may deliberately emit a broken value);
// otherwise slot [11] is derived from the object's node. Callers hold s.mu.
func (s *Server) metaLocked(obj *Object, shockURL string, explicitURL bool) []any {
	dir, name := obj.Path, ""
	if i := strings.LastIndex(obj.Path, "/"); i >= 0 {
		dir, name = obj.Path[:i+1], obj.Path[i+1:]
	}
	if !explicitURL && obj.NodeID != "" {
		shockURL = s.srv.URL + "/shock_api/node/" + obj.NodeID
	}
	meta := obj.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	auto := map[string]any{}
	if obj.Type == "folder" {
		auto["is_folder"] = "1"
	}
	return []any{
		name,                   // 0 ObjectName
		obj.Type,               // 1 ObjectType
		dir,                    // 2 FullObjectPath (directory, trailing /)
		"2026-08-20T12:00:00Z", // 3 creation_time
		obj.ID,                 // 4 ObjectID
		"tester@bvbrc",         // 5 object_owner
		obj.Size,               // 6 ObjectSize
		meta,                   // 7 UserMetadata
		auto,                   // 8 AutoMetadata
		"o",                    // 9 user_permission
		"n",                    // 10 global_permission
		shockURL,               // 11 shockurl
	}
}

// ---- Shock ---------------------------------------------------------------

func (s *Server) handleShock(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/shock_api/node/")

	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	put := ShockPut{
		NodeID:           id,
		Method:           r.Method,
		Authorization:    r.Header.Get("Authorization"),
		ContentLength:    r.ContentLength,
		TransferEncoding: append([]string(nil), r.TransferEncoding...),
	}

	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, "bad content type: "+err.Error(), http.StatusBadRequest)
		return
	}

	if s.HoldShockBody != nil {
		if release := s.HoldShockBody(id); release != nil {
			// Headers are in; the body is never read. Note that Go's server
			// only notices a client disconnect once the body has been
			// consumed, so this must be released by the test, not by the
			// request context.
			<-release
			s.mu.Lock()
			s.heldReturned++
			s.mu.Unlock()
			http.Error(w, `{"status":500,"error":["upload abandoned"],"data":null}`, http.StatusInternalServerError)
			return
		}
	}

	raw := &countingReader{r: r.Body}
	part, err := multipart.NewReader(raw, params["boundary"]).NextPart()
	if err != nil {
		http.Error(w, "no multipart part: "+err.Error(), http.StatusBadRequest)
		return
	}
	put.FormField = part.FormName()
	put.Filename = part.FileName()
	put.Body, err = io.ReadAll(part)
	if err != nil {
		http.Error(w, "reading part: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Consume the closing boundary so RawLength covers the whole body.
	_, _ = io.Copy(io.Discard, raw)
	put.RawLength = raw.n

	s.mu.Lock()
	s.puts = append(s.puts, put)
	node, ok := s.nodes[id]
	if !ok {
		s.mu.Unlock()
		http.Error(w, `{"status":404,"error":["Node not found"],"data":null}`, http.StatusNotFound)
		return
	}
	if node.HasFile {
		// P6: a node's file is immutable once set.
		s.mu.Unlock()
		http.Error(w, `{"status":400,"error":["node file already set"],"data":null}`, http.StatusBadRequest)
		return
	}
	node.HasFile = true
	node.Filename = put.Filename
	node.Body = put.Body
	s.mu.Unlock()

	status, body := http.StatusOK, defaultEnvelope(id, put)
	if s.ShockReply != nil {
		if st, b := s.ShockReply(put); st != 0 {
			status, body = st, b
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// defaultEnvelope is the reply a healthy Shock returns for a file PUT.
func defaultEnvelope(id string, put ShockPut) string {
	sum := md5.Sum(put.Body)
	env := map[string]any{
		"status": 200,
		"error":  nil,
		"data": map[string]any{
			"id": id,
			"file": map[string]any{
				"name":     put.Filename,
				"size":     len(put.Body),
				"checksum": map[string]string{"md5": hex.EncodeToString(sum[:])},
			},
		},
	}
	b, _ := json.Marshal(env)
	return string(b)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/download/")

	s.mu.Lock()
	node, ok := s.nodes[id]
	var body []byte
	if ok {
		body = append([]byte(nil), node.Body...)
	}
	s.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(body)
}
