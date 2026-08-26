package bvbrc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"
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

	// Content is the file content (nil for folders or upload nodes).
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

// WorkspaceUploadFile uploads file bytes to the workspace, binary-safe.
//
// It follows the service's own upload protocol, the one implemented by
// scripts/ws-create.pl --useshock and used by the BV-BRC clients:
//
//  1. Workspace.create the destination with createUploadNodes set, which
//     allocates a Shock node and returns its URL in ObjectMeta slot [11];
//  2. PUT the raw bytes to that URL as multipart form field "upload", with
//     an "Authorization: OAuth <token>" header.
//
// Do not be tempted back to Workspace.create's inline Content field: it is a
// JSON string, and encoding/json replaces every byte that is not valid UTF-8
// with U+FFFD (3 bytes for 1), silently destroying every binary file that goes
// through it. That was issue #172.
func (c *Client) WorkspaceUploadFile(ctx context.Context, wsPath string, data []byte, objType WorkspaceObjectType) (*WorkspaceObject, error) {
	obj, err := c.WorkspaceCreate(ctx, WorkspaceCreateInput{
		Path:              wsPath,
		Type:              objType,
		Overwrite:         true,
		CreateUploadNodes: true,
	})
	if err != nil {
		return nil, err
	}

	if obj.ShockURL == "" {
		// Never fall back to the inline path: for a binary payload that would
		// silently store corrupt bytes. Fail loudly instead.
		return nil, NewError("WorkspaceUploadFile",
			fmt.Sprintf("no Shock upload URL returned for %s (ObjectMeta slot 11 empty)", wsPath))
	}

	if err := c.shockUpload(ctx, obj.ShockURL, path.Base(wsPath), data); err != nil {
		return nil, WrapError("WorkspaceUploadFile", err)
	}

	return obj, nil
}

// shockUpload PUTs data to a Shock node URL as multipart form field "upload".
// Mirrors scripts/ws-create.pl: a multipart/form-data body sent with the PUT
// method and an "OAuth <token>" Authorization header.
func (c *Client) shockUpload(ctx context.Context, shockURL, filename string, data []byte) error {
	if filename == "" {
		filename = "upload"
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("upload", filename)
	if err != nil {
		return fmt.Errorf("building multipart body: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("building multipart body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("building multipart body: %w", err)
	}

	// bytes.Reader gives the request a known Content-Length; Shock is not
	// verified to accept a chunked body.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, shockURL, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return fmt.Errorf("creating Shock request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.config.Token != "" {
		req.Header.Set("Authorization", "OAuth "+c.config.Token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Shock upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	return nil
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

	// The API returns [[url1], [url2], ...] — an array of single-element arrays,
	// one per requested path, in the same order.
	var rawResult [][]string
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceGetDownloadURL", fmt.Errorf("unmarshaling result: %w", err))
	}

	result := make(map[string]string, len(paths))
	for i, urls := range rawResult {
		if i < len(paths) && len(urls) > 0 {
			result[paths[i]] = urls[0]
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
