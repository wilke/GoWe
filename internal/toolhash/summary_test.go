package toolhash

import (
	"strings"
	"testing"
)

func hasFlag(flags []string, prefix string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

func TestSummarize_ScriptsVerbatimAndBasics(t *testing.T) {
	tool := map[string]any{
		"class": "CommandLineTool", "baseCommand": []any{"python3", "run.py"},
		"requirements": map[string]any{
			"DockerRequirement": map[string]any{"dockerPull": "python.sif"},
			"InitialWorkDirRequirement": map[string]any{"listing": []any{
				map[string]any{"entryname": "run.py", "entry": "import sys\nprint('hi')\n"},
			}},
			"EnvVarRequirement": map[string]any{"envDef": map[string]any{"A": "b"}},
		},
		"inputs":  map[string]any{"x": map[string]any{"type": "string"}},
		"outputs": map[string]any{"o": map[string]any{"type": "string"}},
	}
	s := Summarize(tool)
	if s.ImageRef != "python.sif" || s.Class != "CommandLineTool" {
		t.Errorf("basics: %+v", s)
	}
	if len(s.Scripts) != 1 || s.Scripts[0].Content != "import sys\nprint('hi')\n" || s.Scripts[0].Source != "InitialWorkDirRequirement:run.py" {
		t.Errorf("scripts must be verbatim: %+v", s.Scripts)
	}
	if len(s.EnvVars) != 1 || s.EnvVars[0] != "A=b" {
		t.Errorf("env: %v", s.EnvVars)
	}
	if len(s.Flags) != 0 {
		t.Errorf("clean tool should have no flags: %v", s.Flags)
	}
	if strings.Join(s.RequirementClasses, ",") != "DockerRequirement,EnvVarRequirement,InitialWorkDirRequirement" {
		t.Errorf("classes: %v", s.RequirementClasses)
	}
}

func TestSummarize_Flags(t *testing.T) {
	cases := []struct {
		name string
		tool map[string]any
		want string
	}{
		{"stdout field", map[string]any{"class": "CommandLineTool", "stdout": "out.txt"}, FlagStdoutOutput},
		{"stderr field", map[string]any{"class": "CommandLineTool", "stderr": "err.txt"}, FlagStderrOutput},
		{"stdout type", map[string]any{"class": "CommandLineTool",
			"outputs": map[string]any{"o": map[string]any{"type": "stdout"}}}, FlagStdoutOutput},
		{"stderr type", map[string]any{"class": "CommandLineTool",
			"outputs": map[string]any{"o": map[string]any{"type": "stderr"}}}, FlagStderrOutput},
		{"2>&1 in arguments", map[string]any{"class": "CommandLineTool", "baseCommand": "sh",
			"requirements": map[string]any{"ShellCommandRequirement": map[string]any{}},
			"arguments":    []any{"run.sh 2>&1"}}, FlagStreamRedirect},
		{"redirect to file", map[string]any{"class": "CommandLineTool", "baseCommand": "sh",
			"requirements": map[string]any{"ShellCommandRequirement": map[string]any{}},
			"arguments":    []any{"run.sh > log.txt"}}, FlagStreamRedirect},
		{"verbose flag", map[string]any{"class": "CommandLineTool", "baseCommand": []any{"curl", "--verbose"}}, FlagVerboseDebug},
		{"debug flag", map[string]any{"class": "CommandLineTool", "baseCommand": "tool",
			"arguments": []any{"--debug"}}, FlagVerboseDebug},
		{"set -x script", map[string]any{"class": "CommandLineTool",
			"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{
				map[string]any{"entryname": "r.sh", "entry": "#!/bin/bash\nset -x\ncurl x\n"}}}}}, FlagVerboseDebug},
		{"printenv script", map[string]any{"class": "CommandLineTool",
			"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{
				map[string]any{"entryname": "r.sh", "entry": "#!/bin/sh\nprintenv\n"}}}}}, FlagPrintsEnv},
		{"print environ", map[string]any{"class": "CommandLineTool",
			"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{
				map[string]any{"entryname": "r.py", "entry": "import os\nprint(os.environ)\n"}}}}}, FlagPrintsEnv},
		{"print headers", map[string]any{"class": "CommandLineTool",
			"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{
				map[string]any{"entryname": "r.py", "entry": "print(resp.request.headers)\n"}}}}}, FlagPrintsEnv},
		{"input derived script", map[string]any{"class": "CommandLineTool",
			"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{
				map[string]any{"entryname": "r.py", "entry": "$(inputs.code)"}}}}}, FlagInputDerived},
		{"input derived baseCommand", map[string]any{"class": "CommandLineTool", "baseCommand": "$(inputs.cmd)"}, FlagInputDerived},
		{"network", map[string]any{"class": "CommandLineTool",
			"requirements": map[string]any{"NetworkAccess": map[string]any{"networkAccess": true}}}, FlagNetworkAccess},
		{"shell", map[string]any{"class": "CommandLineTool",
			"hints": map[string]any{"ShellCommandRequirement": map[string]any{}}}, FlagShellCommandReq},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Summarize(c.tool)
			if !hasFlag(s.Flags, c.want) {
				t.Errorf("want flag %q, got %v", c.want, s.Flags)
			}
		})
	}
}

func TestSummarize_NoFalsePositives(t *testing.T) {
	// A python script using ">" comparison, reading (not printing) the token.
	tool := map[string]any{"class": "CommandLineTool", "baseCommand": "python3",
		"requirements": map[string]any{"InitialWorkDirRequirement": map[string]any{"listing": []any{
			map[string]any{"entryname": "r.py", "entry": "import os\ntok = os.environ['BVBRC_TOKEN']\nif len(tok) > 3:\n    pass\n"}}}}}
	s := Summarize(tool)
	if hasFlag(s.Flags, FlagStreamRedirect) || hasFlag(s.Flags, FlagPrintsEnv) {
		t.Errorf("unexpected flags: %v", s.Flags)
	}
}
