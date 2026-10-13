package toolhash

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/me/gowe/pkg/model"
)

// Review flag prefixes. They are stable so UIs and tests can match on them.
const (
	FlagStdoutOutput    = "declared-stdout"
	FlagStderrOutput    = "declared-stderr"
	FlagStreamRedirect  = "stream-redirection"
	FlagVerboseDebug    = "verbose-debug"
	FlagPrintsEnv       = "prints-env-or-headers"
	FlagInputDerived    = "input-derived-executable"
	FlagNetworkAccess   = "network-access"
	FlagShellCommandReq = "shell-command"
)

var (
	// redirectRe matches shell stream redirection: 2>&1, >&2, &>, 1>file, >file, >>file, |&.
	redirectRe = regexp.MustCompile(`2>&1|&>>?|>&[12]|\|&|(?:^|\s)[12]?>>?\s*[^\s&=<>]`)
	// verboseRe matches verbose/debug switches and shell tracing.
	verboseRe = regexp.MustCompile(`(?i)(?:^|\s)(?:--verbose|--debug|--trace|-v{1,3}|-x)(?:\s|=|$)|\bset\s+-[a-z]*x|logging\.DEBUG|debuglevel|\bDEBUG\s*=\s*(?:1|true)`)
	// envPrintRe matches commands/code that print the environment or request headers.
	envPrintRe = regexp.MustCompile(`(?i)(?:^|[\s;|&(])(?:printenv|env)(?:\s*$|\s*[|;&>)])|export\s+-p|(?:print|pprint|echo|log\w*|dump\w*)\s*\([^\n]*environ|json\.dumps\(\s*(?:dict\()?os\.environ|/proc/\S*/environ|(?:print|echo|log\w*|dump\w*)[^\n]*(?:headers?|authorization)|curl[^\n]*(?:\s-v\b|--verbose|--dump-header|-D\s*-)`)
	// inputRe matches CWL expressions that read inputs.
	inputRe = regexp.MustCompile(`\$\(\s*inputs\.|\$\{[^}]*inputs\.|\binputs\.[A-Za-z_]`)
)

// Summarize builds the review summary for a tool: its command, image, classes
// and every piece of executable content verbatim, plus review flags.
func Summarize(tool map[string]any) model.ToolSummary {
	doc, err := normalize(tool)
	if err != nil {
		doc = map[string]any{}
	}
	s := model.ToolSummary{ImageRef: ImageRef(doc)}
	s.Class, _ = doc["class"].(string)
	s.Label, _ = doc["label"].(string)
	s.RequirementClasses = sectionClasses(doc["requirements"])
	s.HintClasses = sectionClasses(doc["hints"])

	switch bc := doc["baseCommand"].(type) {
	case string:
		s.BaseCommand = []string{bc}
	case []any:
		for _, e := range bc {
			s.BaseCommand = append(s.BaseCommand, fmt.Sprint(e))
		}
	}
	if args, ok := doc["arguments"].([]any); ok {
		for _, a := range args {
			switch v := a.(type) {
			case string:
				s.Arguments = append(s.Arguments, v)
			case map[string]any:
				if vf, ok := v["valueFrom"].(string); ok {
					s.Arguments = append(s.Arguments, vf)
				} else if pos, ok := v["prefix"].(string); ok {
					s.Arguments = append(s.Arguments, pos)
				}
			}
		}
	}
	for id := range mapOf(doc["inputs"]) {
		s.Inputs = append(s.Inputs, id)
	}
	sort.Strings(s.Inputs)
	outputs := mapOf(doc["outputs"])
	for id := range outputs {
		s.Outputs = append(s.Outputs, id)
	}
	sort.Strings(s.Outputs)

	reqs := lookupClassBodies(doc, "requirements", "hints")

	// EnvVarRequirement
	if body := reqs["EnvVarRequirement"]; body != nil {
		switch ev := body["envDef"].(type) {
		case map[string]any:
			for k, v := range ev {
				s.EnvVars = append(s.EnvVars, fmt.Sprintf("%s=%v", k, v))
			}
		case []any:
			for _, e := range ev {
				if m, ok := e.(map[string]any); ok {
					s.EnvVars = append(s.EnvVars, fmt.Sprintf("%v=%v", m["envName"], m["envValue"]))
				}
			}
		}
		sort.Strings(s.EnvVars)
	}

	// NetworkAccess
	if body := reqs["NetworkAccess"]; body != nil {
		switch v := body["networkAccess"].(type) {
		case bool:
			s.NetworkAccess = v
		case string:
			s.NetworkAccess = v != "" && v != "false"
		default:
			s.NetworkAccess = v != nil
		}
		if s.NetworkAccess {
			s.Flags = append(s.Flags, FlagNetworkAccess+": tool declares NetworkAccess; it can send data (including the token) over the network")
		}
	}

	// InitialWorkDirRequirement scripts, verbatim.
	var scripts []model.ScriptEntry
	if body := reqs["InitialWorkDirRequirement"]; body != nil {
		scripts = append(scripts, iwdrScripts(body["listing"])...)
	}
	s.Scripts = scripts

	shellReq := reqs["ShellCommandRequirement"] != nil
	if shellReq {
		s.Flags = append(s.Flags, FlagShellCommandReq+": ShellCommandRequirement present; arguments are interpreted by a shell")
	}

	// Output handling checklist.
	if so, _ := doc["stdout"].(string); so != "" {
		s.Flags = append(s.Flags, FlagStdoutOutput+": tool declares stdout: "+so+" (captured stdout is a declared output and is staged out to the user's destination)")
	}
	if se, _ := doc["stderr"].(string); se != "" {
		s.Flags = append(s.Flags, FlagStderrOutput+": tool declares stderr: "+se+" (stderr is where tracebacks and HTTP error bodies land; staged out to the user's destination)")
	}
	soField, _ := doc["stdout"].(string)
	seField, _ := doc["stderr"].(string)
	for _, id := range s.Outputs {
		o, _ := outputs[id].(map[string]any)
		switch o["type"] {
		case "stdout":
			if soField != "" {
				continue // already flagged via the stdout: field
			}
			s.Flags = append(s.Flags, FlagStdoutOutput+": output "+id+" has type stdout (staged out to the user's destination)")
		case "stderr":
			if seField != "" {
				continue
			}
			s.Flags = append(s.Flags, FlagStderrOutput+": output "+id+" has type stderr (staged out to the user's destination)")
		}
	}

	// Text scanning. Command text always; script bodies as well.
	cmdText := append(append([]string{}, s.BaseCommand...), s.Arguments...)
	type scanItem struct{ where, text string }
	var items []scanItem
	for _, c := range cmdText {
		items = append(items, scanItem{"command", c})
	}
	for _, sc := range scripts {
		items = append(items, scanItem{sc.Source, sc.Content})
	}
	// Join command words so multi-word patterns ("bash -x", "2>&1") match.
	if len(cmdText) > 0 {
		items = append(items, scanItem{"command line", strings.Join(cmdText, " ")})
	}
	seen := map[string]bool{}
	add := func(flag string) {
		if !seen[flag] {
			seen[flag] = true
			s.Flags = append(s.Flags, flag)
		}
	}
	for _, it := range items {
		isScript := it.where != "command" && it.where != "command line"
		if (shellReq && !isScript || isShellScript(it.where, it.text)) && redirectRe.MatchString(it.text) {
			add(FlagStreamRedirect + ": shell stream redirection in " + it.where + " (stderr/stdout may be merged into a declared output or file)")
		}
		if verboseRe.MatchString(it.text) {
			add(FlagVerboseDebug + ": verbose/debug flag or tracing in " + it.where + " (verbose output can include request headers)")
		}
		if envPrintRe.MatchString(it.text) {
			add(FlagPrintsEnv + ": " + it.where + " references the environment or request headers (may print the token)")
		}
	}

	// Input-derived executable content (blank-check tools).
	if len(s.BaseCommand) > 0 && inputRe.MatchString(s.BaseCommand[0]) {
		add(FlagInputDerived + ": baseCommand is derived from inputs")
	}
	for _, a := range s.Arguments {
		if shellReq && inputRe.MatchString(a) {
			add(FlagInputDerived + ": shell argument derived from inputs: " + a)
		}
	}
	for _, sc := range scripts {
		if inputRe.MatchString(sc.Source) || inputRe.MatchString(sc.Content) {
			add(FlagInputDerived + ": " + sc.Source + " is derived from inputs (the job, not the tool, decides what runs)")
		}
	}
	return s
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func sectionClasses(v any) []string {
	var out []string
	switch s := v.(type) {
	case map[string]any:
		for c := range s {
			out = append(out, c)
		}
	case []any:
		for _, item := range s {
			if m, ok := item.(map[string]any); ok {
				if c, _ := m["class"].(string); c != "" {
					out = append(out, c)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// lookupClassBodies returns class -> body for the given sections (earlier
// sections win), tolerating both map and list encodings.
func lookupClassBodies(doc map[string]any, sections ...string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, sect := range sections {
		switch s := doc[sect].(type) {
		case map[string]any:
			for c, b := range s {
				if _, ok := out[c]; ok {
					continue
				}
				m, _ := b.(map[string]any)
				if m == nil {
					m = map[string]any{}
				}
				out[c] = m
			}
		case []any:
			for _, item := range s {
				if m, ok := item.(map[string]any); ok {
					c, _ := m["class"].(string)
					if _, dup := out[c]; c != "" && !dup {
						out[c] = m
					}
				}
			}
		}
	}
	return out
}

func iwdrScripts(listing any) []model.ScriptEntry {
	var out []model.ScriptEntry
	add := func(name string, entry any) {
		src := "InitialWorkDirRequirement"
		if name != "" {
			src += ":" + name
		}
		switch e := entry.(type) {
		case string:
			out = append(out, model.ScriptEntry{Source: src, Content: e})
		case map[string]any:
			if c, ok := e["contents"].(string); ok {
				out = append(out, model.ScriptEntry{Source: src, Content: c})
			} else if ent, ok := e["entry"]; ok {
				if str, ok := ent.(string); ok {
					out = append(out, model.ScriptEntry{Source: src, Content: str})
				}
			}
		}
	}
	switch l := listing.(type) {
	case string: // a single expression yielding the listing
		out = append(out, model.ScriptEntry{Source: "InitialWorkDirRequirement:listing", Content: l})
	case []any:
		for _, item := range l {
			switch it := item.(type) {
			case string:
				add("", it)
			case map[string]any:
				if _, isDirent := it["entry"]; isDirent {
					name, _ := it["entryname"].(string)
					add(name, it)
				} else {
					name, _ := it["basename"].(string)
					add(name, it)
				}
			}
		}
	}
	return out
}

// isShellScript reports whether script text looks like a shell script
// (shebang naming a shell, or a .sh entry name), so redirection checks do not
// fire on the ">" operator of other languages.
func isShellScript(where, text string) bool {
	if strings.HasSuffix(where, ".sh") || strings.HasSuffix(where, ".bash") {
		return true
	}
	if strings.HasPrefix(text, "#!") {
		line := text
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			line = text[:i]
		}
		return strings.Contains(line, "sh")
	}
	return false
}
