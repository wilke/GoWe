package toolhash

import (
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/toolmerge"
	"github.com/me/gowe/pkg/cwl"
)

const toolBody = `    class: CommandLineTool
    baseCommand: [python3, run.py]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
      InitialWorkDirRequirement:
        listing:
          - entryname: run.py
            entry: |
              import os, sys
              print("hello")
    hints:
      gowe:Execution:
        inject_bvbrc_token: true
    inputs:
      x: string
    outputs:
      out:
        type: string
        outputBinding:
          outputEval: ok
`

func packed(wfExtra, stepExtra string) string {
	return `cwlVersion: v1.2
$graph:
  - id: helper
` + toolBody + `  - id: main
    class: Workflow
` + wfExtra + `    inputs:
      x: string
    outputs: {}
    steps:
      s:
        run: "#helper"
` + stepExtra + `        in:
          x: x
        out: [out]
`
}

func bare() string {
	return "cwlVersion: v1.2\n$namespaces:\n  gowe: https://github.com/wilke/GoWe#\nid: helper\n" + strings.ReplaceAll(toolBody, "\n    ", "\n")[4:]
}

func digestStep(t *testing.T, raw, path string, jobReqs []any) *StepResult {
	t.Helper()
	r, err := DigestForStep(raw, path, jobReqs)
	if err != nil {
		t.Fatalf("DigestForStep: %v", err)
	}
	return r
}

func TestDigest_FormatAndDeterminism(t *testing.T) {
	r := digestStep(t, packed("", ""), "s", nil)
	if !strings.HasPrefix(r.Hash, "sha256:") || len(r.Hash) != len("sha256:")+64 {
		t.Fatalf("bad digest %q", r.Hash)
	}
	for i := 0; i < 5; i++ {
		if again := digestStep(t, packed("", ""), "s", nil); again.Hash != r.Hash {
			t.Fatalf("digest not deterministic: %s vs %s", again.Hash, r.Hash)
		}
	}
	if r.ImageRef != "python.sif" {
		t.Errorf("ImageRef = %q", r.ImageRef)
	}
	if len(r.Inherited) != 0 || r.Hash != r.PristineHash {
		t.Errorf("no inheritance expected: inherited=%v pristine=%s hash=%s", r.Inherited, r.PristineHash, r.Hash)
	}
}

func TestDigest_IgnoresIdCwlVersionNamespaces(t *testing.T) {
	a := map[string]any{"class": "CommandLineTool", "baseCommand": "echo", "id": "a", "cwlVersion": "v1.2",
		"$namespaces": map[string]any{"x": "y"}, "$schemas": []any{"s"}}
	b := map[string]any{"class": "CommandLineTool", "baseCommand": "echo", "id": "zzz", "cwlVersion": "v1.0"}
	ha, _ := Digest(a)
	hb, _ := Digest(b)
	if ha != hb {
		t.Errorf("id/cwlVersion/$namespaces must not matter: %s vs %s", ha, hb)
	}
}

func TestDigest_ByteChangeChangesHash(t *testing.T) {
	a := map[string]any{"class": "CommandLineTool", "baseCommand": "echo",
		"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{map[string]any{"entryname": "r.py", "entry": "print(1)"}}}}}
	b := map[string]any{"class": "CommandLineTool", "baseCommand": "echo",
		"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{map[string]any{"entryname": "r.py", "entry": "print(2)"}}}}}
	ha, _ := Digest(a)
	hb, _ := Digest(b)
	if ha == hb {
		t.Error("one-byte script change must change the hash")
	}
	// Input maps are not mutated.
	if _, ok := a["id"]; ok {
		t.Error("Digest mutated its input")
	}
}

func TestDigest_Preimage(t *testing.T) {
	tool := map[string]any{"class": "CommandLineTool", "baseCommand": "echo", "id": "x"}
	canon, err := Canonical(tool)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"baseCommand":"echo","class":"CommandLineTool"}` {
		t.Errorf("canonical = %s", canon)
	}
}

func TestStandalone_EqualsGowePackedCopy(t *testing.T) {
	// A tool registered on its own...
	standalone, err := DigestStandalone(bare(), "")
	if err != nil {
		t.Fatal(err)
	}
	// ...and the same tool inlined by resolveGoweRefs into a packed $graph
	// (id assigned by the resolver, no $namespaces, different cwlVersion).
	inlined := "cwlVersion: v1.2\n$graph:\n  - id: predict-structure\n" + toolBody + `  - id: main
    class: Workflow
    inputs: {x: string}
    outputs: {}
    steps:
      s:
        run: "#predict-structure"
        in: {x: x}
        out: [out]
`
	viaStep := digestStep(t, inlined, "s", nil)
	if standalone.Hash != viaStep.Hash {
		t.Errorf("standalone %s != packed copy %s", standalone.Hash, viaStep.Hash)
	}
	packedStandalone, err := DigestStandalone(inlined, "predict-structure")
	if err != nil {
		t.Fatal(err)
	}
	if packedStandalone.Hash != standalone.Hash {
		t.Errorf("packed standalone %s != bare standalone %s", packedStandalone.Hash, standalone.Hash)
	}
}

func TestStandalone_AmbiguousNeedsToolID(t *testing.T) {
	raw := `cwlVersion: v1.2
$graph:
  - id: a
    class: CommandLineTool
    baseCommand: a
    inputs: {}
    outputs: {}
  - id: b
    class: CommandLineTool
    baseCommand: b
    inputs: {}
    outputs: {}
  - id: main
    class: Workflow
    inputs: {}
    outputs: {}
    steps:
      s:
        run: "#a"
        in: {}
        out: []
`
	if _, err := DigestStandalone(raw, ""); err == nil {
		t.Error("two tools without a tool id must be an error")
	}
	if _, err := DigestStandalone(raw, "b"); err != nil {
		t.Errorf("explicit tool id: %v", err)
	}
	if _, err := DigestStandalone(raw, "zz"); err == nil {
		t.Error("unknown tool id must be an error")
	}
}

func TestBenignInheritedRequirementsAgree(t *testing.T) {
	base := digestStep(t, packed("", ""), "s", nil)
	wfExtra := `    requirements:
      ScatterFeatureRequirement: {}
      StepInputExpressionRequirement: {}
      SubworkflowFeatureRequirement: {}
      MultipleInputFeatureRequirement: {}
      ResourceRequirement:
        coresMin: 4
      SchemaDefRequirement:
        types: []
      LoadListingRequirement:
        loadListing: no_listing
      InlineJavascriptRequirement: {}
    hints:
      ResourceRequirement:
        ramMin: 100
      WorkReuse:
        enableReuse: false
      ToolTimeLimit:
        timelimit: 60
      gowe:ResourceData:
        datasets: []
`
	r := digestStep(t, packed(wfExtra, ""), "s", nil)
	if r.Hash != base.Hash {
		t.Errorf("benign inherited classes changed the hash\n base=%s\n got =%s", base.Hash, r.Hash)
	}
	if len(r.Inherited) != 0 {
		t.Errorf("benign classes must not be reported as inherited: %v", r.Inherited)
	}
	// And at the step level.
	stepExtra := `        requirements:
          ResourceRequirement:
            coresMin: 2
        hints:
          LoadListingRequirement:
            loadListing: shallow_listing
`
	if r := digestStep(t, packed("", stepExtra), "s", nil); r.Hash != base.Hash {
		t.Errorf("benign step-level classes changed the hash")
	}
}

func TestInheritedEnvVarRequirementFails_NamesClass(t *testing.T) {
	base := digestStep(t, packed("", ""), "s", nil)
	wfExtra := `    requirements:
      EnvVarRequirement:
        envDef:
          FOO: bar
`
	r := digestStep(t, packed(wfExtra, ""), "s", nil)
	if r.Hash == base.Hash {
		t.Fatal("inherited EnvVarRequirement must change the hash")
	}
	if !reflect.DeepEqual(r.Inherited, []string{"EnvVarRequirement"}) {
		t.Errorf("Inherited = %v", r.Inherited)
	}
	if r.PristineHash != base.Hash {
		t.Error("PristineHash must equal the unmerged digest")
	}
	// Inherited via a step hint too.
	stepExtra := `        hints:
          NetworkAccess:
            networkAccess: true
`
	r2 := digestStep(t, packed("", stepExtra), "s", nil)
	if r2.Hash == base.Hash || !reflect.DeepEqual(r2.Inherited, []string{"NetworkAccess"}) {
		t.Errorf("step hint NetworkAccess: hash same=%v inherited=%v", r2.Hash == base.Hash, r2.Inherited)
	}
}

func TestJobRequirementsChangeHash(t *testing.T) {
	base := digestStep(t, packed("", ""), "s", nil)
	r := digestStep(t, packed("", ""), "s", []any{map[string]any{"class": "DockerRequirement", "dockerPull": "evil.sif"}})
	if r.Hash == base.Hash {
		t.Error("job cwl:requirements DockerRequirement must change the hash")
	}
	if r.ImageRef != "evil.sif" {
		t.Errorf("ImageRef = %q", r.ImageRef)
	}
}

func TestStepDockerHintDoesNotOverrideToolRequirement(t *testing.T) {
	// requirements beat hints in the merge; the hash reflects what runs.
	stepExtra := `        hints:
          DockerRequirement:
            dockerPull: other.sif
`
	r := digestStep(t, packed("", stepExtra), "s", nil)
	if r.ImageRef != "python.sif" {
		t.Errorf("tool requirement DockerRequirement must win, got %q", r.ImageRef)
	}
}

func TestExpressionLibIsContent(t *testing.T) {
	plain := map[string]any{"class": "CommandLineTool", "baseCommand": "x"}
	empty := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"requirements": map[string]any{"InlineJavascriptRequirement": map[string]any{}}}
	lib := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"requirements": map[string]any{"InlineJavascriptRequirement": map[string]any{"expressionLib": []any{"function f(){}"}}}}
	hp, _ := Digest(plain)
	he, _ := Digest(empty)
	hl, _ := Digest(lib)
	if hp != he {
		t.Error("InlineJavascriptRequirement without lib is benign")
	}
	if hp == hl {
		t.Error("InlineJavascriptRequirement with expressionLib is content")
	}
}

func TestBenignStrippedSymmetricallyFromHintsAndRequirements(t *testing.T) {
	a := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"requirements": map[string]any{"ResourceRequirement": map[string]any{"coresMin": 1}}}
	b := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"hints": map[string]any{"ResourceRequirement": map[string]any{"coresMin": 9}}}
	c := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"requirements": map[string]any{}, "hints": map[string]any{}}
	ha, _ := Digest(a)
	hb, _ := Digest(b)
	hc, _ := Digest(c)
	if ha != hb || ha != hc {
		t.Errorf("benign/empty sections must agree: %s %s %s", ha, hb, hc)
	}
	// List-form requirements are handled too.
	l := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"requirements": []any{map[string]any{"class": "ResourceRequirement", "coresMin": 1}}}
	if hl, _ := Digest(l); hl != ha {
		t.Error("list-form benign requirement must be stripped")
	}
}

func TestDefaultDeny_UnknownClassIsContent(t *testing.T) {
	a := map[string]any{"class": "CommandLineTool", "baseCommand": "x"}
	b := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"hints": map[string]any{"cwltool:Something": map[string]any{"a": 1}}}
	ha, _ := Digest(a)
	hb, _ := Digest(b)
	if ha == hb {
		t.Error("unknown classes must be hashed")
	}
}

const nestedCWL = `cwlVersion: v1.2
$graph:
  - id: helper
` + toolBody + `  - id: inner
    class: Workflow
    requirements:
      EnvVarRequirement:
        envDef:
          INNER: "1"
    inputs: {x: string}
    outputs: {}
    steps:
      annotate:
        run: "#helper"
        in: {x: x}
        out: [out]
  - id: main
    class: Workflow
    requirements:
      EnvVarRequirement:
        envDef:
          OUTER: "1"
    inputs: {x: string}
    outputs: {}
    steps:
      cga:
        run: "#inner"
        in: {x: x}
        out: []
`

func TestNestedStepUsesChildRoot(t *testing.T) {
	r := digestStep(t, nestedCWL, "cga/annotate", nil)
	if !reflect.DeepEqual(r.Inherited, []string{"EnvVarRequirement"}) {
		t.Fatalf("Inherited = %v", r.Inherited)
	}
	env := r.Tool["requirements"].(map[string]any)["EnvVarRequirement"].(map[string]any)["envDef"].(map[string]any)
	if _, ok := env["INNER"]; !ok {
		t.Errorf("nested step must merge the sub-workflow's requirements, got %v", env)
	}
	if _, ok := env["OUTER"]; ok {
		t.Errorf("parent workflow requirements must NOT be inherited by nested steps: %v", env)
	}
	// Hash equals what dispatch computes: the child's raw CWL, merged at its "main".
	child, err := toolmerge.ChildGraphForStep(nestedCWL, "cga")
	if err != nil {
		t.Fatal(err)
	}
	direct := digestStep(t, child, "annotate", nil)
	if direct.Hash != r.Hash {
		t.Errorf("nested digest %s != child-root digest %s", r.Hash, direct.Hash)
	}
}

func TestDigestForStep_Errors(t *testing.T) {
	if _, err := DigestForStep(packed("", ""), "missing", nil); err == nil {
		t.Error("missing step must error")
	}
	if _, err := DigestForStep(nestedCWL, "cga", nil); err == nil {
		t.Error("a step running a sub-workflow is not a tool step")
	}
	if _, err := DigestForStep(packed("", ""), "", nil); err == nil {
		t.Error("empty path must error")
	}
	if _, err := DigestForStep(packed("", ""), "s/x", nil); err == nil {
		t.Error("descending into a tool step must error")
	}
}

// TestPreMergeMapsByteIdentical asserts the map DigestStandalone hashes is
// byte-identical to the pre-merge map the scheduler starts from
// (toolmerge.StructToToolMap on the parsed tool).
func TestPreMergeMapsByteIdentical(t *testing.T) {
	g, err := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil))).ParseGraph([]byte(packed("", "")))
	if err != nil {
		t.Fatal(err)
	}
	sched, err := toolmerge.StructToToolMap(g.Tools["helper"])
	if err != nil {
		t.Fatal(err)
	}
	sa, err := DigestStandaloneGraph(g, "helper")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(sched)
	b, _ := json.Marshal(sa.Tool)
	if string(a) != string(b) {
		t.Errorf("pre-merge maps differ:\n%s\n%s", a, b)
	}
	step := digestStep(t, packed("", ""), "s", nil)
	if step.PristineHash != sa.Hash || step.Hash != sa.Hash {
		t.Errorf("standalone %s vs step pristine %s / merged %s", sa.Hash, step.PristineHash, step.Hash)
	}
}

func TestImageRef_MatchesCwltoolGetDockerImage(t *testing.T) {
	cases := []*cwl.CommandLineTool{
		{Requirements: map[string]any{"DockerRequirement": map[string]any{"dockerPull": "a.sif"}}},
		{Hints: map[string]any{"DockerRequirement": map[string]any{"dockerPull": "b.sif"}}},
		{Requirements: map[string]any{"DockerRequirement": map[string]any{"dockerPull": "a.sif"}},
			Hints: map[string]any{"DockerRequirement": map[string]any{"dockerPull": "b.sif"}}},
		{Requirements: map[string]any{"DockerRequirement": map[string]any{}}},
		{},
	}
	for i, clt := range cases {
		m, err := toolmerge.StructToToolMap(clt)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := ImageRef(m), cwltool.GetDockerImage(clt); got != want {
			t.Errorf("case %d: ImageRef=%q GetDockerImage=%q", i, got, want)
		}
	}
}

func TestToolRequestsToken(t *testing.T) {
	g, err := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil))).ParseGraph([]byte(packed("", "")))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := toolmerge.StructToToolMap(g.Tools["helper"])
	if !ToolRequestsToken(m) {
		t.Error("helper requests the token")
	}
	if ToolRequestsToken(map[string]any{}) {
		t.Error("empty tool does not")
	}
}

func TestListFormEqualsMapForm(t *testing.T) {
	mapForm := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"requirements": map[string]any{"DockerRequirement": map[string]any{"dockerPull": "a.sif"}},
		"hints":        map[string]any{"gowe:Execution": map[string]any{"inject_bvbrc_token": true}}}
	listForm := map[string]any{"class": "CommandLineTool", "baseCommand": "x",
		"requirements": []any{map[string]any{"class": "DockerRequirement", "dockerPull": "a.sif"}},
		"hints":        []any{map[string]any{"class": "gowe:Execution", "inject_bvbrc_token": true}}}
	hm, _ := Digest(mapForm)
	hl, _ := Digest(listForm)
	if hm != hl {
		t.Errorf("list-form %s != map-form %s", hl, hm)
	}
}

func TestIsExecutionClass(t *testing.T) {
	for _, c := range []string{"gowe:Execution", "goweHint", "https://github.com/wilke/GoWe#Execution"} {
		if !IsExecutionClass(c) {
			t.Errorf("%q should match", c)
		}
	}
	if IsExecutionClass("DockerRequirement") {
		t.Error("DockerRequirement is not Execution")
	}
}
