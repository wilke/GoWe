package cwl

import "testing"

func TestDocument_Class(t *testing.T) {
	tests := []struct {
		name string
		doc  Document
		want string
	}{
		{"workflow", Document{"class": "Workflow"}, "Workflow"},
		{"tool", Document{"class": "CommandLineTool"}, "CommandLineTool"},
		{"missing", Document{}, ""},
		{"wrong type", Document{"class": 42}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.doc.Class(); got != tt.want {
				t.Errorf("Class() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDocument_ID(t *testing.T) {
	tests := []struct {
		name string
		doc  Document
		want string
	}{
		{"present", Document{"id": "main"}, "main"},
		{"missing", Document{}, ""},
		{"wrong type", Document{"id": 123}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.doc.ID(); got != tt.want {
				t.Errorf("ID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDocument_CWLVersion(t *testing.T) {
	tests := []struct {
		name string
		doc  Document
		want string
	}{
		{"v1.2", Document{"cwlVersion": "v1.2"}, "v1.2"},
		{"missing", Document{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.doc.CWLVersion(); got != tt.want {
				t.Errorf("CWLVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDocument_IsGraph(t *testing.T) {
	tests := []struct {
		name string
		doc  Document
		want bool
	}{
		{"graph present", Document{"$graph": []any{}}, true},
		{"no graph", Document{"class": "Workflow"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.doc.IsGraph(); got != tt.want {
				t.Errorf("IsGraph() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDocument_Graph(t *testing.T) {
	t.Run("valid graph", func(t *testing.T) {
		doc := Document{
			"$graph": []any{
				map[string]any{"id": "tool1", "class": "CommandLineTool"},
				map[string]any{"id": "main", "class": "Workflow"},
			},
		}
		g := doc.Graph()
		if len(g) != 2 {
			t.Fatalf("Graph() returned %d entries, want 2", len(g))
		}
		if g[0].ID() != "tool1" {
			t.Errorf("Graph()[0].ID() = %q, want %q", g[0].ID(), "tool1")
		}
		if g[1].Class() != "Workflow" {
			t.Errorf("Graph()[1].Class() = %q, want %q", g[1].Class(), "Workflow")
		}
	})

	t.Run("no graph", func(t *testing.T) {
		doc := Document{"class": "Workflow"}
		if g := doc.Graph(); g != nil {
			t.Errorf("Graph() = %v, want nil", g)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		doc := Document{"$graph": "not a slice"}
		if g := doc.Graph(); g != nil {
			t.Errorf("Graph() = %v, want nil", g)
		}
	})

	t.Run("skips non-map entries", func(t *testing.T) {
		doc := Document{
			"$graph": []any{
				map[string]any{"id": "tool1"},
				"not a map",
				42,
			},
		}
		g := doc.Graph()
		if len(g) != 1 {
			t.Fatalf("Graph() returned %d entries, want 1", len(g))
		}
	})
}

func TestDocument_Steps(t *testing.T) {
	t.Run("valid steps", func(t *testing.T) {
		doc := Document{
			"steps": map[string]any{
				"assemble": map[string]any{
					"run": "#bvbrc-assembly",
				},
				"annotate": map[string]any{
					"run": "#bvbrc-annotation",
				},
			},
		}
		steps := doc.Steps()
		if len(steps) != 2 {
			t.Fatalf("Steps() returned %d entries, want 2", len(steps))
		}
		if _, ok := steps["assemble"]; !ok {
			t.Error("Steps() missing 'assemble'")
		}
		if _, ok := steps["annotate"]; !ok {
			t.Error("Steps() missing 'annotate'")
		}
	})

	t.Run("no steps", func(t *testing.T) {
		doc := Document{"class": "CommandLineTool"}
		if s := doc.Steps(); s != nil {
			t.Errorf("Steps() = %v, want nil", s)
		}
	})

	t.Run("wrong type", func(t *testing.T) {
		doc := Document{"steps": "not a map"}
		steps := doc.Steps()
		if len(steps) != 0 {
			t.Errorf("Steps() returned %d entries, want 0", len(steps))
		}
	})

	t.Run("skips non-map values", func(t *testing.T) {
		doc := Document{
			"steps": map[string]any{
				"good": map[string]any{"run": "tool.cwl"},
				"bad":  "not a map",
			},
		}
		steps := doc.Steps()
		if len(steps) != 1 {
			t.Fatalf("Steps() returned %d entries, want 1", len(steps))
		}
		if _, ok := steps["good"]; !ok {
			t.Error("Steps() missing 'good'")
		}
	})
}
