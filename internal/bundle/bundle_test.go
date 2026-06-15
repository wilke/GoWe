package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testdataPath(rel string) string {
	// Tests run from the package directory, testdata is at repo root
	return filepath.Join("..", "..", "testdata", rel)
}

// ResolveFilePaths must NOT mangle remote URI locations (ws://, shock://,
// http(s)://): they are not local paths and must survive bundling intact so the
// BV-BRC executor can flatten them to a workspace path, and so worker/local
// execution can resolve them. Only file:// and scheme-less locations are local.
func TestResolveFilePaths_PreservesRemoteURIs(t *testing.T) {
	cases := []struct {
		name     string
		class    string
		location string
	}{
		{"workspace file", "File", "ws:///user@bvbrc/home/data/contigs.fasta"},
		{"workspace dir", "Directory", "ws:///user@bvbrc/home/output"},
		{"shock file", "File", "shock://p3.theseed.org/node/abc123"},
		{"https file", "File", "https://example.com/data/x.fasta"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]any{"class": tc.class, "location": tc.location}
			out, ok := ResolveFilePaths(in, "/jobs/basedir").(map[string]any)
			if !ok {
				t.Fatalf("ResolveFilePaths returned %T, want map", out)
			}
			if got := out["location"]; got != tc.location {
				t.Errorf("location = %q, want %q (must be preserved, not joined with baseDir)", got, tc.location)
			}
			// A remote URI must not acquire a local baseDir-joined path.
			if p, ok := out["path"].(string); ok && strings.Contains(p, "/jobs/basedir") {
				t.Errorf("path = %q leaked baseDir for a remote URI", p)
			}
		})
	}
}

// file:// and relative locations remain local-path resolution (regression guard
// that the remote-URI fix did not change local behavior).
func TestResolveFilePaths_LocalStillResolved(t *testing.T) {
	t.Run("file:// localized", func(t *testing.T) {
		in := map[string]any{"class": "File", "location": "file:///tmp/staged/x.fasta"}
		out := ResolveFilePaths(in, "/jobs/basedir").(map[string]any)
		if got := out["location"]; got != "file:///tmp/staged/x.fasta" {
			t.Errorf("location = %q, want file:///tmp/staged/x.fasta", got)
		}
		if got := out["path"]; got != "/tmp/staged/x.fasta" {
			t.Errorf("path = %q, want /tmp/staged/x.fasta", got)
		}
	})
	t.Run("relative joined with baseDir", func(t *testing.T) {
		in := map[string]any{"class": "File", "location": "sub/x.fasta"}
		out := ResolveFilePaths(in, "/jobs/basedir").(map[string]any)
		if got := out["location"]; got != "/jobs/basedir/sub/x.fasta" {
			t.Errorf("location = %q, want /jobs/basedir/sub/x.fasta", got)
		}
	})
}

func TestBundle_SeparateFiles(t *testing.T) {
	result, err := Bundle(testdataPath("separate/pipeline.cwl"))
	if err != nil {
		t.Fatalf("Bundle() error: %v", err)
	}

	if result.Name != "pipeline" {
		t.Errorf("Name = %q, want pipeline", result.Name)
	}

	// Parse the packed output
	var doc map[string]any
	if err := yaml.Unmarshal(result.Packed, &doc); err != nil {
		t.Fatalf("unmarshal packed: %v", err)
	}

	// Should have cwlVersion at top level
	if v, ok := doc["cwlVersion"].(string); !ok || v != "v1.2" {
		t.Errorf("cwlVersion = %v, want v1.2", doc["cwlVersion"])
	}

	// Should have $graph
	graph, ok := doc["$graph"].([]any)
	if !ok {
		t.Fatal("expected $graph array")
	}

	// Should have 3 entries: 2 tools + 1 workflow
	if len(graph) != 3 {
		t.Errorf("$graph length = %d, want 3", len(graph))
	}

	// Check that tools have IDs
	ids := map[string]bool{}
	for _, entry := range graph {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		ids[id] = true
	}

	if !ids["bvbrc-assembly"] {
		t.Error("missing bvbrc-assembly in $graph")
	}
	if !ids["bvbrc-annotation"] {
		t.Error("missing bvbrc-annotation in $graph")
	}
	if !ids["main"] {
		t.Error("missing main workflow in $graph")
	}

	// Check that run: references are now fragments
	packed := string(result.Packed)
	if !strings.Contains(packed, `"#bvbrc-assembly"`) && !strings.Contains(packed, "'#bvbrc-assembly'") && !strings.Contains(packed, "\"#bvbrc-assembly\"") {
		// yaml.v3 may serialize differently, check the raw map
		for _, entry := range graph {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if m["id"] == "main" {
				steps, ok := m["steps"].(map[string]any)
				if !ok {
					t.Fatal("workflow missing steps")
				}
				for stepName, stepVal := range steps {
					step, ok := stepVal.(map[string]any)
					if !ok {
						continue
					}
					runRef, _ := step["run"].(string)
					if !strings.HasPrefix(runRef, "#") {
						t.Errorf("step %q run = %q, want # prefix", stepName, runRef)
					}
				}
			}
		}
	}
}

func TestBundle_AlreadyPacked(t *testing.T) {
	result, err := Bundle(testdataPath("packed/pipeline-packed.cwl"))
	if err != nil {
		t.Fatalf("Bundle() error: %v", err)
	}

	if result.Name != "pipeline-packed" {
		t.Errorf("Name = %q, want pipeline-packed", result.Name)
	}

	// Should pass through as-is
	var doc map[string]any
	if err := yaml.Unmarshal(result.Packed, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc["$graph"]; !ok {
		t.Error("expected $graph in packed output")
	}
}

func TestBundle_MissingFile(t *testing.T) {
	_, err := Bundle(testdataPath("nonexistent.cwl"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "read workflow") {
		t.Errorf("error = %q, want 'read workflow' in message", err.Error())
	}
}

func TestBundle_MissingToolRef(t *testing.T) {
	// Create a temp workflow referencing a nonexistent tool
	dir := t.TempDir()
	wf := `cwlVersion: v1.2
class: Workflow
inputs: {}
steps:
  step1:
    run: missing-tool.cwl
    in: {}
    out: []
outputs: {}
`
	wfPath := filepath.Join(dir, "bad.cwl")
	os.WriteFile(wfPath, []byte(wf), 0644)

	_, err := Bundle(wfPath)
	if err == nil {
		t.Fatal("expected error for missing tool reference")
	}
	if !strings.Contains(err.Error(), "missing-tool.cwl") {
		t.Errorf("error = %q, want 'missing-tool.cwl' in message", err.Error())
	}
}

func TestBundle_BareTool(t *testing.T) {
	dir := t.TempDir()
	tool := `cwlVersion: v1.2
class: CommandLineTool
baseCommand: ["echo"]
inputs:
  message:
    type: string
outputs:
  output:
    type: stdout
`
	toolPath := filepath.Join(dir, "tool.cwl")
	os.WriteFile(toolPath, []byte(tool), 0644)

	result, err := Bundle(toolPath)
	if err != nil {
		t.Fatalf("Bundle() error: %v", err)
	}

	if result.Name != "tool" {
		t.Errorf("Name = %q, want tool", result.Name)
	}

	// Parse the packed output
	var doc map[string]any
	if err := yaml.Unmarshal(result.Packed, &doc); err != nil {
		t.Fatalf("unmarshal packed: %v", err)
	}

	// Should have $graph with tool and synthetic workflow
	graph, ok := doc["$graph"].([]any)
	if !ok {
		t.Fatal("expected $graph array")
	}

	if len(graph) != 2 {
		t.Errorf("$graph length = %d, want 2 (tool + workflow)", len(graph))
	}

	// Check IDs
	ids := map[string]bool{}
	for _, entry := range graph {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		ids[id] = true
	}

	if !ids["tool"] {
		t.Error("missing tool in $graph")
	}
	if !ids["main"] {
		t.Error("missing main workflow in $graph")
	}
}

func TestBundle_UnknownClass(t *testing.T) {
	dir := t.TempDir()
	doc := `cwlVersion: v1.2
class: UnknownClass
inputs: {}
outputs: {}
`
	path := filepath.Join(dir, "unknown.cwl")
	os.WriteFile(path, []byte(doc), 0644)

	_, err := Bundle(path)
	if err == nil {
		t.Fatal("expected error for unknown class")
	}
	if !strings.Contains(err.Error(), "expected class") {
		t.Errorf("error = %q, want 'expected class' in message", err.Error())
	}
}

func TestNameFromPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"pipeline.cwl", "pipeline"},
		{"/path/to/my-workflow.cwl", "my-workflow"},
		{"workflow.yaml", "workflow"},
		{"simple", "simple"},
	}
	for _, tt := range tests {
		if got := nameFromPath(tt.input); got != tt.want {
			t.Errorf("nameFromPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
