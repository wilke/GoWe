package parser

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

func testParser() *Parser {
	return New(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func loadTestdata(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("load testdata %q: %v", rel, err)
	}
	return data
}

func TestParseGraph_PackedPipeline(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	if graph.CWLVersion != "v1.2" {
		t.Errorf("CWLVersion = %q, want v1.2", graph.CWLVersion)
	}

	if graph.Workflow == nil {
		t.Fatal("Workflow is nil")
	}

	if len(graph.Tools) != 2 {
		t.Errorf("Tools count = %d, want 2", len(graph.Tools))
	}

	if _, ok := graph.Tools["bvbrc-assembly"]; !ok {
		t.Error("missing tool bvbrc-assembly")
	}
	if _, ok := graph.Tools["bvbrc-annotation"]; !ok {
		t.Error("missing tool bvbrc-annotation")
	}
}

func TestParseGraph_WorkflowInputs(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	wf := graph.Workflow
	if len(wf.Inputs) != 4 {
		t.Fatalf("inputs count = %d, want 4", len(wf.Inputs))
	}

	// Shorthand inputs: "reads_r1: File"
	r1, ok := wf.Inputs["reads_r1"]
	if !ok {
		t.Fatal("missing input reads_r1")
	}
	if r1.Type != "File" {
		t.Errorf("reads_r1.Type = %q, want File", r1.Type)
	}

	sci, ok := wf.Inputs["scientific_name"]
	if !ok {
		t.Fatal("missing input scientific_name")
	}
	if sci.Type != "string" {
		t.Errorf("scientific_name.Type = %q, want string", sci.Type)
	}

	tid, ok := wf.Inputs["taxonomy_id"]
	if !ok {
		t.Fatal("missing input taxonomy_id")
	}
	if tid.Type != "int" {
		t.Errorf("taxonomy_id.Type = %q, want int", tid.Type)
	}
}

func TestParseGraph_WorkflowOutputs(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	wf := graph.Workflow
	if len(wf.Outputs) != 1 {
		t.Fatalf("outputs count = %d, want 1", len(wf.Outputs))
	}

	genome, ok := wf.Outputs["genome"]
	if !ok {
		t.Fatal("missing output genome")
	}
	if genome.Type != "File" {
		t.Errorf("genome.Type = %q, want File", genome.Type)
	}
	if genome.OutputSource != "annotate/annotated_genome" {
		t.Errorf("genome.OutputSource = %q, want annotate/annotated_genome", genome.OutputSource)
	}
}

func TestParseGraph_Steps(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	wf := graph.Workflow
	if len(wf.Steps) != 2 {
		t.Fatalf("steps count = %d, want 2", len(wf.Steps))
	}

	assemble, ok := wf.Steps["assemble"]
	if !ok {
		t.Fatal("missing step assemble")
	}
	if assemble.Run != "#bvbrc-assembly" {
		t.Errorf("assemble.Run = %q, want #bvbrc-assembly", assemble.Run)
	}
	if len(assemble.In) != 2 {
		t.Errorf("assemble.In count = %d, want 2", len(assemble.In))
	}
	if r1, ok := assemble.In["read1"]; !ok || r1.Source != "reads_r1" {
		t.Errorf("assemble.In[read1].Source = %q, want reads_r1", assemble.In["read1"].Source)
	}
	if len(assemble.Out) != 1 || assemble.Out[0] != "contigs" {
		t.Errorf("assemble.Out = %v, want [contigs]", assemble.Out)
	}

	annotate, ok := wf.Steps["annotate"]
	if !ok {
		t.Fatal("missing step annotate")
	}
	if annotate.Run != "#bvbrc-annotation" {
		t.Errorf("annotate.Run = %q, want #bvbrc-annotation", annotate.Run)
	}
	if len(annotate.In) != 3 {
		t.Errorf("annotate.In count = %d, want 3", len(annotate.In))
	}
	// Source referencing another step's output.
	if c, ok := annotate.In["contigs"]; !ok || c.Source != "assemble/contigs" {
		t.Errorf("annotate.In[contigs].Source = %q, want assemble/contigs", annotate.In["contigs"].Source)
	}
}

func TestParseGraph_ToolInputsAndOutputs(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	tool := graph.Tools["bvbrc-assembly"]
	if tool == nil {
		t.Fatal("tool bvbrc-assembly is nil")
	}
	if tool.Class != "CommandLineTool" {
		t.Errorf("Class = %q, want CommandLineTool", tool.Class)
	}

	// Inputs: expanded form {type: File}
	if len(tool.Inputs) != 3 {
		t.Fatalf("tool inputs = %d, want 3", len(tool.Inputs))
	}
	r1 := tool.Inputs["read1"]
	if r1.Type != "File" {
		t.Errorf("read1.Type = %q, want File", r1.Type)
	}
	recipe := tool.Inputs["recipe"]
	if recipe.Type != "string" {
		t.Errorf("recipe.Type = %q, want string", recipe.Type)
	}
	if recipe.Default != "auto" {
		t.Errorf("recipe.Default = %v, want auto", recipe.Default)
	}

	// Outputs with outputBinding.
	if len(tool.Outputs) != 1 {
		t.Fatalf("tool outputs = %d, want 1", len(tool.Outputs))
	}
	contigs := tool.Outputs["contigs"]
	if contigs.Type != "File" {
		t.Errorf("contigs.Type = %q, want File", contigs.Type)
	}
	if contigs.OutputBinding == nil || contigs.OutputBinding.Glob != "*.contigs.fasta" {
		t.Errorf("contigs.OutputBinding.Glob = %v, want *.contigs.fasta", contigs.OutputBinding)
	}
}

func TestParseGraph_GoWeHint(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	tool := graph.Tools["bvbrc-assembly"]
	if tool.Hints == nil {
		t.Fatal("tool.Hints is nil")
	}
	gowe, ok := tool.Hints["goweHint"].(map[string]any)
	if !ok {
		t.Fatal("goweHint not found in hints")
	}
	if gowe["bvbrc_app_id"] != "GenomeAssembly2" {
		t.Errorf("bvbrc_app_id = %v, want GenomeAssembly2", gowe["bvbrc_app_id"])
	}
}

func TestParseGraph_InvalidYAML(t *testing.T) {
	p := testParser()
	_, err := p.ParseGraph([]byte("{{{{invalid"))
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestParseGraph_NoGraph(t *testing.T) {
	p := testParser()
	_, err := p.ParseGraph([]byte("cwlVersion: v1.2\nclass: Workflow\n"))
	if err == nil {
		t.Error("expected error for missing $graph")
	}
}

func TestParseGraph_NoWorkflow(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
$graph:
  - id: tool1
    class: CommandLineTool
    inputs: {}
    outputs: {}
`)
	_, err := p.ParseGraph(data)
	if err == nil {
		t.Error("expected error for missing Workflow in $graph")
	}
}

func TestParseGraph_MultipleWorkflows(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
$graph:
  - id: wf1
    class: Workflow
    inputs: {}
    outputs: {}
    steps: {}
  - id: wf2
    class: Workflow
    inputs: {}
    outputs: {}
    steps: {}
`)
	_, err := p.ParseGraph(data)
	if err == nil {
		t.Error("expected error for multiple Workflows")
	}
}

func TestParseGraph_ExpandedInputs(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      reads:
        type: File
        doc: "Input reads"
        default: null
    outputs:
      result:
        type: File
        outputSource: step1/output
    steps:
      step1:
        run: "#tool1"
        in:
          input1:
            source: reads
        out: [output]
  - id: tool1
    class: CommandLineTool
    baseCommand: echo
    inputs:
      input1:
        type: File
    outputs:
      output:
        type: File
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	reads := graph.Workflow.Inputs["reads"]
	if reads.Type != "File" {
		t.Errorf("reads.Type = %q, want File", reads.Type)
	}
	if reads.Doc != "Input reads" {
		t.Errorf("reads.Doc = %q, want 'Input reads'", reads.Doc)
	}

	// Step input with expanded source.
	si := graph.Workflow.Steps["step1"].In["input1"]
	if si.Source != "reads" {
		t.Errorf("step1.In[input1].Source = %q, want reads", si.Source)
	}
}

func TestToModel_PackedPipeline(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	mw, err := p.ToModel(graph, "test-pipeline")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}

	if mw.Name != "test-pipeline" {
		t.Errorf("Name = %q, want test-pipeline", mw.Name)
	}
	if mw.CWLVersion != "v1.2" {
		t.Errorf("CWLVersion = %q, want v1.2", mw.CWLVersion)
	}
	if len(mw.Inputs) != 4 {
		t.Errorf("Inputs count = %d, want 4", len(mw.Inputs))
	}
	if len(mw.Outputs) != 1 {
		t.Errorf("Outputs count = %d, want 1", len(mw.Outputs))
	}
	if len(mw.Steps) != 2 {
		t.Errorf("Steps count = %d, want 2", len(mw.Steps))
	}
}

func TestToModel_DependsOn(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	mw, err := p.ToModel(graph, "test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}

	// Find steps by ID.
	stepMap := make(map[string]int)
	for i, s := range mw.Steps {
		stepMap[s.ID] = i
	}

	assemble := mw.Steps[stepMap["assemble"]]
	if len(assemble.DependsOn) != 0 {
		t.Errorf("assemble.DependsOn = %v, want []", assemble.DependsOn)
	}

	annotate := mw.Steps[stepMap["annotate"]]
	if len(annotate.DependsOn) != 1 || annotate.DependsOn[0] != "assemble" {
		t.Errorf("annotate.DependsOn = %v, want [assemble]", annotate.DependsOn)
	}
}

func TestToModel_ToolInlineResolved(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	mw, err := p.ToModel(graph, "test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}

	for _, step := range mw.Steps {
		if step.ToolInline == nil {
			t.Errorf("step %q: ToolInline is nil", step.ID)
			continue
		}
		if step.ToolInline.Class != "CommandLineTool" {
			t.Errorf("step %q: ToolInline.Class = %q, want CommandLineTool", step.ID, step.ToolInline.Class)
		}
	}
}

func TestToModel_StepHints(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	mw, err := p.ToModel(graph, "test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}

	for _, step := range mw.Steps {
		if step.Hints == nil {
			t.Errorf("step %q: Hints is nil", step.ID)
			continue
		}
		if step.Hints.BVBRCAppID == "" {
			t.Errorf("step %q: BVBRCAppID is empty", step.ID)
		}
		if step.Hints.ExecutorType != "bvbrc" {
			t.Errorf("step %q: ExecutorType = %q, want bvbrc", step.ID, step.Hints.ExecutorType)
		}
	}
}

func TestToModel_InputsSorted(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	mw, err := p.ToModel(graph, "test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}

	for i := 1; i < len(mw.Inputs); i++ {
		if mw.Inputs[i].ID < mw.Inputs[i-1].ID {
			t.Errorf("inputs not sorted: %q comes after %q", mw.Inputs[i].ID, mw.Inputs[i-1].ID)
		}
	}
}

// Test helper functions.

func TestStringField(t *testing.T) {
	m := map[string]any{"name": "hello", "count": 42, "flag": true}
	if got := stringField(m, "name"); got != "hello" {
		t.Errorf("stringField(name) = %q, want hello", got)
	}
	if got := stringField(m, "missing"); got != "" {
		t.Errorf("stringField(missing) = %q, want empty", got)
	}
	// YAML can parse type: int as the integer 0, so stringField should handle it.
	if got := stringField(m, "count"); got != "42" {
		t.Errorf("stringField(count) = %q, want 42", got)
	}
}

func TestStringSlice(t *testing.T) {
	m := map[string]any{
		"items": []any{"a", "b", "c"},
	}
	got := stringSlice(m, "items")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("stringSlice = %v, want [a b c]", got)
	}

	if got := stringSlice(m, "missing"); got != nil {
		t.Errorf("stringSlice(missing) = %v, want nil", got)
	}
}

func TestExtractStepHints(t *testing.T) {
	hints := map[string]any{
		"goweHint": map[string]any{
			"bvbrc_app_id": "GenomeAssembly2",
			"executor":     "bvbrc",
		},
	}
	got := extractStepHints(hints)
	if got == nil {
		t.Fatal("extractStepHints returned nil")
	}
	if got.BVBRCAppID != "GenomeAssembly2" {
		t.Errorf("BVBRCAppID = %q, want GenomeAssembly2", got.BVBRCAppID)
	}
	if got.ExecutorType != "bvbrc" {
		t.Errorf("ExecutorType = %q, want bvbrc", got.ExecutorType)
	}
}

func TestExtractStepHints_Nil(t *testing.T) {
	if got := extractStepHints(nil); got != nil {
		t.Errorf("extractStepHints(nil) = %v, want nil", got)
	}
}

func TestExtractStepHints_NoGoweHint(t *testing.T) {
	hints := map[string]any{"DockerRequirement": map[string]any{}}
	if got := extractStepHints(hints); got != nil {
		t.Errorf("extractStepHints = %v, want nil", got)
	}
}

func TestExtractStepHints_DockerRequirement(t *testing.T) {
	hints := map[string]any{
		"DockerRequirement": map[string]any{
			"dockerPull": "ubuntu:22.04",
		},
	}
	got := extractStepHints(hints)
	if got == nil {
		t.Fatal("extractStepHints returned nil")
	}
	if got.DockerImage != "ubuntu:22.04" {
		t.Errorf("DockerImage = %q, want ubuntu:22.04", got.DockerImage)
	}
	if got.ExecutorType != model.ExecutorTypeContainer {
		t.Errorf("ExecutorType = %q, want container", got.ExecutorType)
	}
}

func TestExtractStepHints_GoweDockerOverridesDockerRequirement(t *testing.T) {
	hints := map[string]any{
		"goweHint": map[string]any{
			"docker_image": "custom:latest",
			"executor":     "container",
		},
		"DockerRequirement": map[string]any{
			"dockerPull": "ubuntu:22.04",
		},
	}
	got := extractStepHints(hints)
	if got == nil {
		t.Fatal("extractStepHints returned nil")
	}
	if got.DockerImage != "custom:latest" {
		t.Errorf("DockerImage = %q, want custom:latest (goweHint should take precedence)", got.DockerImage)
	}
}

func TestExtractStepHints_GoweHintDockerImage(t *testing.T) {
	hints := map[string]any{
		"goweHint": map[string]any{
			"executor":     "container",
			"docker_image": "biocontainers/samtools:1.17",
		},
	}
	got := extractStepHints(hints)
	if got == nil {
		t.Fatal("extractStepHints returned nil")
	}
	if got.DockerImage != "biocontainers/samtools:1.17" {
		t.Errorf("DockerImage = %q, want biocontainers/samtools:1.17", got.DockerImage)
	}
	if got.ExecutorType != model.ExecutorTypeContainer {
		t.Errorf("ExecutorType = %q, want container", got.ExecutorType)
	}
}

func TestComputeDependsOn(t *testing.T) {
	tests := []struct {
		name     string
		inputs   []cwl.StepInput
		wfInputs map[string]bool
		want     []string
	}{
		{
			name: "no dependencies",
			inputs: []cwl.StepInput{
				{Source: "reads_r1"},
				{Source: "reads_r2"},
			},
			wfInputs: map[string]bool{"reads_r1": true, "reads_r2": true},
			want:     nil,
		},
		{
			name: "one dependency",
			inputs: []cwl.StepInput{
				{Source: "assemble/contigs"},
				{Source: "scientific_name"},
			},
			wfInputs: map[string]bool{"scientific_name": true},
			want:     []string{"assemble"},
		},
		{
			name: "deduplicated",
			inputs: []cwl.StepInput{
				{Source: "assemble/contigs"},
				{Source: "assemble/stats"},
			},
			wfInputs: map[string]bool{},
			want:     []string{"assemble"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert cwl.StepInput to model.StepInput.
			var modelInputs []model.StepInput
			for _, si := range tt.inputs {
				modelInputs = append(modelInputs, model.StepInput{Source: si.Source})
			}
			got := computeDependsOn(modelInputs, tt.wfInputs)
			if len(got) != len(tt.want) {
				t.Fatalf("computeDependsOn = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("computeDependsOn[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
