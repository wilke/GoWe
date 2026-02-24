package parser

import (
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

func makeWorkflow(steps map[string]cwl.Step) *cwl.Workflow {
	return &cwl.Workflow{
		ID:    "main",
		Class: "Workflow",
		Steps: steps,
	}
}

func TestBuildDAG_LinearPipeline(t *testing.T) {
	wf := makeWorkflow(map[string]cwl.Step{
		"assemble": {
			Run: "#tool1",
			In:  map[string]cwl.StepInput{"read1": {Sources: []string{"reads_r1"}}},
			Out: []string{"contigs"},
		},
		"annotate": {
			Run: "#tool2",
			In: map[string]cwl.StepInput{
				"contigs": {Sources: []string{"assemble/contigs"}},
			},
			Out: []string{"genome"},
		},
	})

	dag, err := BuildDAG(wf)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	// Order: assemble before annotate.
	if len(dag.Order) != 2 {
		t.Fatalf("Order length = %d, want 2", len(dag.Order))
	}
	if dag.Order[0] != "assemble" || dag.Order[1] != "annotate" {
		t.Errorf("Order = %v, want [assemble annotate]", dag.Order)
	}

	// Edges: annotate depends on assemble.
	if deps := dag.Edges["annotate"]; len(deps) != 1 || deps[0] != "assemble" {
		t.Errorf("annotate deps = %v, want [assemble]", deps)
	}
	if deps := dag.Edges["assemble"]; len(deps) != 0 {
		t.Errorf("assemble deps = %v, want []", deps)
	}
}

func TestBuildDAG_ParallelSteps(t *testing.T) {
	wf := makeWorkflow(map[string]cwl.Step{
		"step_a": {
			Run: "#tool1",
			In:  map[string]cwl.StepInput{"input": {Sources: []string{"wf_input"}}},
			Out: []string{"output"},
		},
		"step_b": {
			Run: "#tool2",
			In:  map[string]cwl.StepInput{"input": {Sources: []string{"wf_input"}}},
			Out: []string{"output"},
		},
	})

	dag, err := BuildDAG(wf)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	if len(dag.Order) != 2 {
		t.Fatalf("Order length = %d, want 2", len(dag.Order))
	}

	// Both are independent — no dependencies.
	if len(dag.Edges["step_a"]) != 0 {
		t.Errorf("step_a deps = %v, want []", dag.Edges["step_a"])
	}
	if len(dag.Edges["step_b"]) != 0 {
		t.Errorf("step_b deps = %v, want []", dag.Edges["step_b"])
	}
}

func TestBuildDAG_DiamondShape(t *testing.T) {
	// A -> B, A -> C, B -> D, C -> D
	wf := makeWorkflow(map[string]cwl.Step{
		"a": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"input"}}},
			Out: []string{"out"},
		},
		"b": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"a/out"}}},
			Out: []string{"out"},
		},
		"c": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"a/out"}}},
			Out: []string{"out"},
		},
		"d": {
			Run: "#t",
			In: map[string]cwl.StepInput{
				"x": {Sources: []string{"b/out"}},
				"y": {Sources: []string{"c/out"}},
			},
			Out: []string{"out"},
		},
	})

	dag, err := BuildDAG(wf)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	if len(dag.Order) != 4 {
		t.Fatalf("Order length = %d, want 4", len(dag.Order))
	}

	// a must come first, d must come last.
	if dag.Order[0] != "a" {
		t.Errorf("Order[0] = %q, want a", dag.Order[0])
	}
	if dag.Order[3] != "d" {
		t.Errorf("Order[3] = %q, want d", dag.Order[3])
	}

	// d depends on both b and c.
	dDeps := dag.Edges["d"]
	if len(dDeps) != 2 {
		t.Fatalf("d deps = %v, want 2 entries", dDeps)
	}
}

func TestBuildDAG_CycleDetected(t *testing.T) {
	// A -> B -> A (cycle)
	wf := makeWorkflow(map[string]cwl.Step{
		"a": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"b/out"}}},
			Out: []string{"out"},
		},
		"b": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"a/out"}}},
			Out: []string{"out"},
		},
	})

	_, err := BuildDAG(wf)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %q, want to contain 'cycle'", err.Error())
	}
}

func TestBuildDAG_SelfLoop(t *testing.T) {
	wf := makeWorkflow(map[string]cwl.Step{
		"a": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"a/out"}}},
			Out: []string{"out"},
		},
	})

	_, err := BuildDAG(wf)
	if err == nil {
		t.Fatal("expected self-loop error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error = %q, want to contain 'cycle'", err.Error())
	}
}

func TestBuildDAG_SingleStep(t *testing.T) {
	wf := makeWorkflow(map[string]cwl.Step{
		"only": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"input"}}},
			Out: []string{"out"},
		},
	})

	dag, err := BuildDAG(wf)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	if len(dag.Order) != 1 || dag.Order[0] != "only" {
		t.Errorf("Order = %v, want [only]", dag.Order)
	}
}

func TestBuildDAG_ThreeStepChain(t *testing.T) {
	// a -> b -> c
	wf := makeWorkflow(map[string]cwl.Step{
		"a": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"input"}}},
			Out: []string{"out"},
		},
		"b": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"a/out"}}},
			Out: []string{"out"},
		},
		"c": {
			Run: "#t",
			In:  map[string]cwl.StepInput{"x": {Sources: []string{"b/out"}}},
			Out: []string{"out"},
		},
	})

	dag, err := BuildDAG(wf)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	if len(dag.Order) != 3 {
		t.Fatalf("Order = %v, want 3 entries", dag.Order)
	}
	if dag.Order[0] != "a" || dag.Order[1] != "b" || dag.Order[2] != "c" {
		t.Errorf("Order = %v, want [a b c]", dag.Order)
	}
}

func TestBuildDAG_PackedPipeline(t *testing.T) {
	p := testParser()
	data := loadTestdata(t, "packed/pipeline-packed.cwl")
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	dag, err := BuildDAG(graph.Workflow)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	if len(dag.Order) != 2 {
		t.Fatalf("Order = %v, want 2 entries", dag.Order)
	}
	if dag.Order[0] != "assemble" || dag.Order[1] != "annotate" {
		t.Errorf("Order = %v, want [assemble annotate]", dag.Order)
	}
}
