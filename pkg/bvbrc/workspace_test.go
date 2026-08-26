package bvbrc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Tests of the unexported tuple parsers live here; everything that drives the
// client over HTTP is in workspace_upload_test.go (package bvbrc_test) against
// the shared bvbrctest fake.

func TestParseWorkspaceObjectTuple_SpecLayout(t *testing.T) {
	// Shaped like WorkspaceImpl.pm's _generate_object_meta: 12 slots, the
	// directory in [2] with a trailing slash, permissions in [9]/[10], the Shock
	// URL in [11].
	tuple := []any{
		"vectors.f32",
		"unspecified",
		"/tester@bvbrc/home/out/",
		"2026-08-20T12:00:00Z",
		"1a2b3c4d-0000-1111-2222-333344445555",
		"tester@bvbrc",
		float64(3948608),
		map[string]any{"run": "42"},
		map[string]any{"is_folder": "0"},
		"o",
		"n",
		"https://example.invalid/services/shock_api/node/abc-123",
	}

	obj, err := parseWorkspaceObjectTuple(tuple)
	if err != nil {
		t.Fatalf("parseWorkspaceObjectTuple: %v", err)
	}

	if obj.Name != "vectors.f32" {
		t.Errorf("Name = %q, want slot [0]", obj.Name)
	}
	if obj.Path != "/tester@bvbrc/home/out/vectors.f32" {
		t.Errorf("Path = %q, want slot [2] joined with slot [0]", obj.Path)
	}
	if obj.Type != WorkspaceTypeUnspecified {
		t.Errorf("Type = %q, want slot [1]", obj.Type)
	}
	if obj.Owner != "tester@bvbrc" {
		t.Errorf("Owner = %q, want slot [5] — not the path in slot [2]", obj.Owner)
	}
	if obj.Size != 3948608 {
		t.Errorf("Size = %d, want slot [6]", obj.Size)
	}
	if obj.UserMetadata["run"] != "42" {
		t.Errorf("UserMetadata = %v, want slot [7]", obj.UserMetadata)
	}
	if obj.UserPermission != "o" || obj.GlobalPermission != "n" {
		t.Errorf("permissions = %q/%q, want slots [9]/[10]", obj.UserPermission, obj.GlobalPermission)
	}
	if obj.ShockURL != "https://example.invalid/services/shock_api/node/abc-123" {
		t.Errorf("ShockURL = %q, want slot [11] — slot [10] is the global permission", obj.ShockURL)
	}
	if obj.CreationTime.IsZero() {
		t.Error("CreationTime not parsed from slot [3]")
	}
	if obj.Error != "" {
		t.Errorf("Error = %q, want empty for a 12-slot tuple", obj.Error)
	}
}

func TestParseWorkspaceObjectTuple_ThirteenSlotsCarryError(t *testing.T) {
	tuple := []any{
		"x", "unspecified", "/tester@bvbrc/home/", "2026-08-20T12:00:00Z",
		"id", "tester@bvbrc", float64(0), map[string]any{}, map[string]any{},
		"o", "n", "", "permission denied",
	}
	obj, err := parseWorkspaceObjectTuple(tuple)
	if err != nil {
		t.Fatalf("parseWorkspaceObjectTuple: %v", err)
	}
	if obj.Error != "permission denied" {
		t.Errorf("Error = %q, want slot [12]", obj.Error)
	}
}

func TestParseWorkspaceGetEntry_MetaDataPair(t *testing.T) {
	// Workspace.spec: get(...) returns list<tuple<ObjectMeta,ObjectData>>.
	meta := []any{
		"manifest.json", "json", "/tester@bvbrc/home/out/", "2026-08-20T12:00:00Z",
		"id-1", "tester@bvbrc", float64(1006), map[string]any{}, map[string]any{},
		"o", "n", "",
	}
	entry := []any{meta, `{"files":[]}`}

	obj, err := parseWorkspaceGetEntry(entry)
	if err != nil {
		t.Fatalf("parseWorkspaceGetEntry: %v", err)
	}
	if obj.Path != "/tester@bvbrc/home/out/manifest.json" {
		t.Errorf("Path = %q, want the path from the meta half of the pair", obj.Path)
	}
	if obj.Data != `{"files":[]}` {
		t.Errorf("Data = %q, want the ObjectData half of the pair", obj.Data)
	}
	if obj.Type != WorkspaceTypeJSON {
		t.Errorf("Type = %q; the meta half was not parsed as ObjectMeta", obj.Type)
	}
}

func TestParseWorkspaceGetEntry_BareMetaTuple(t *testing.T) {
	// Defensive: a bare metadata tuple (no data half) must still parse.
	meta := []any{
		"manifest.json", "json", "/tester@bvbrc/home/out/", "2026-08-20T12:00:00Z",
		"id-1", "tester@bvbrc", float64(1006), map[string]any{}, map[string]any{},
		"o", "n", "",
	}
	obj, err := parseWorkspaceGetEntry(meta)
	if err != nil {
		t.Fatalf("parseWorkspaceGetEntry: %v", err)
	}
	if obj.Path != "/tester@bvbrc/home/out/manifest.json" {
		t.Errorf("Path = %q", obj.Path)
	}
}

// TestShockPutReply_RecordedFixture pins the parser to the envelope a real
// Shock returned (testdata/shock-put-reply.json, recorded by the integration
// test): the fields the upload verification relies on must decode from it.
func TestShockPutReply_RecordedFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "shock-put-reply.json"))
	if err != nil {
		t.Fatal(err)
	}

	var reply shockPutReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	if reply.Status != 200 {
		t.Errorf("Status = %d, want 200", reply.Status)
	}
	if len(reply.Error) != 0 {
		t.Errorf("Error = %v, want none", reply.Error)
	}
	if reply.Data.ID == "" {
		t.Error("Data.ID is empty")
	}
	if reply.Data.File.Name != "fixture.gz" {
		t.Errorf("Data.File.Name = %q, want fixture.gz", reply.Data.File.Name)
	}
	if reply.Data.File.Size != 204888 {
		t.Errorf("Data.File.Size = %d, want 204888", reply.Data.File.Size)
	}
	if md5 := reply.Data.File.Checksum["md5"]; len(md5) != 32 {
		t.Errorf("Data.File.Checksum[md5] = %q, want a 32-hex-digit md5", md5)
	}
}
