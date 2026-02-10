package bvbrc

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Result is [[[obj_tuple], ...]]
	var rawResult [][][]any
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceGet", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(rawResult) == 0 || len(rawResult[0]) == 0 {
		return []WorkspaceObject{}, nil
	}

	objects := make([]WorkspaceObject, 0, len(rawResult[0]))
	for _, tuple := range rawResult[0] {
		obj, err := parseWorkspaceObjectTuple(tuple)
		if err != nil {
			continue
		}
		objects = append(objects, obj)
	}

	return objects, nil
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

// WorkspaceUpload uploads content to the workspace.
func (c *Client) WorkspaceUpload(ctx context.Context, path, content string, objType WorkspaceObjectType) (*WorkspaceObject, error) {
	return c.WorkspaceCreate(ctx, WorkspaceCreateInput{
		Path:      path,
		Type:      objType,
		Content:   &content,
		Overwrite: true,
	})
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

	objects_result := make([]WorkspaceObject, 0, len(rawResult[0]))
	for _, tuple := range rawResult[0] {
		obj, err := parseWorkspaceObjectTuple(tuple)
		if err != nil {
			continue
		}
		objects_result = append(objects_result, obj)
	}

	return objects_result, nil
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

	objects_result := make([]WorkspaceObject, 0, len(rawResult[0]))
	for _, tuple := range rawResult[0] {
		obj, err := parseWorkspaceObjectTuple(tuple)
		if err != nil {
			continue
		}
		objects_result = append(objects_result, obj)
	}

	return objects_result, nil
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

	var rawResult []map[string]string
	if err := json.Unmarshal(resp.Result, &rawResult); err != nil {
		return nil, WrapError("WorkspaceGetDownloadURL", fmt.Errorf("unmarshaling result: %w", err))
	}

	if len(rawResult) == 0 {
		return map[string]string{}, nil
	}

	return rawResult[0], nil
}

// parseWorkspaceObjectTuple parses a workspace object tuple into a WorkspaceObject.
// Tuple format: [path, type, owner, creation_time, id, owner_id, size, user_meta, auto_meta, shock_ref, shock_node, ?data]
func parseWorkspaceObjectTuple(tuple []any) (WorkspaceObject, error) {
	obj := WorkspaceObject{}

	if len(tuple) < 9 {
		return obj, fmt.Errorf("tuple too short: %d elements", len(tuple))
	}

	// Index 0: path
	if s, ok := tuple[0].(string); ok {
		obj.Path = s
	}

	// Index 1: type
	if s, ok := tuple[1].(string); ok {
		obj.Type = WorkspaceObjectType(s)
	}

	// Index 2: owner
	if s, ok := tuple[2].(string); ok {
		obj.Owner = s
	}

	// Index 3: creation_time
	if s, ok := tuple[3].(string); ok {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			obj.CreationTime = t
		}
	}

	// Index 4: id
	if s, ok := tuple[4].(string); ok {
		obj.ID = s
	}

	// Index 5: owner_id
	if s, ok := tuple[5].(string); ok {
		obj.OwnerID = s
	}

	// Index 6: size
	switch v := tuple[6].(type) {
	case float64:
		obj.Size = int64(v)
	case int64:
		obj.Size = v
	case int:
		obj.Size = int64(v)
	}

	// Index 7: user_metadata
	if m, ok := tuple[7].(map[string]any); ok {
		obj.UserMetadata = make(map[string]string)
		for k, v := range m {
			if s, ok := v.(string); ok {
				obj.UserMetadata[k] = s
			}
		}
	}

	// Index 8: auto_metadata
	if m, ok := tuple[8].(map[string]any); ok {
		obj.AutoMetadata = make(map[string]string)
		for k, v := range m {
			if s, ok := v.(string); ok {
				obj.AutoMetadata[k] = s
			}
		}
	}

	// Index 9: shock_ref (if present)
	if len(tuple) > 9 {
		if s, ok := tuple[9].(string); ok {
			obj.ShockRef = s
		}
	}

	// Index 10: shock_node (if present)
	if len(tuple) > 10 {
		if s, ok := tuple[10].(string); ok {
			obj.ShockNodeID = s
		}
	}

	// Index 11: data (if present and not metadata_only)
	if len(tuple) > 11 {
		if s, ok := tuple[11].(string); ok {
			obj.Data = s
		}
	}

	return obj, nil
}
