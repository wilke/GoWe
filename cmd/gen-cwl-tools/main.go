package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/me/gowe/internal/bvbrc"
)

const appServiceURL = "https://p3.theseed.org/services/app_service"

func main() {
	outputDir := flag.String("output-dir", "cwl", "Root output directory for generated CWL files")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	logLevel := slog.LevelWarn
	if *debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	// 1. Resolve token.
	tok, err := bvbrc.ResolveToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	info := bvbrc.ParseToken(tok)
	if info.IsExpired() {
		fmt.Fprintf(os.Stderr, "fatal: BV-BRC token is expired (expiry: %s)\n", info.Expiry.Format(time.RFC3339))
		os.Exit(1)
	}
	fmt.Printf("Token loaded for user %q (expires %s)\n", info.Username, info.Expiry.Format("2006-01-02"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 2. Fetch apps via enumerate_apps.
	cfg := bvbrc.ClientConfig{AppServiceURL: appServiceURL, Token: tok}
	caller := bvbrc.NewHTTPRPCCaller(cfg, logger)

	apps, err := fetchApps(ctx, caller)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Fetched %d apps from BV-BRC\n\n", len(apps))

	// 3. Create output directories.
	toolsDir := filepath.Join(*outputDir, "tools")
	workflowsDir := filepath.Join(*outputDir, "workflows")
	for _, dir := range []string{toolsDir, workflowsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "fatal: mkdir %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	// 4. Generate CWL tools and collect report data.
	sort.Slice(apps, func(i, j int) bool {
		idI, _ := apps[i]["id"].(string)
		idJ, _ := apps[j]["id"].(string)
		return idI < idJ
	})

	var reports []appReport
	for _, app := range apps {
		report := generateCWLTool(app, toolsDir)
		reports = append(reports, report)
		if report.Error != "" {
			fmt.Printf("  SKIP  %s — %s\n", report.AppID, report.Error)
		} else {
			fmt.Printf("  OK    %s — %d inputs\n", report.AppID, len(report.Inputs))
		}
	}

	// 5. Write report.
	reportPath := filepath.Join(*outputDir, "REPORT.md")
	if err := writeReport(reportPath, reports); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: write report: %v\n", err)
		os.Exit(1)
	}

	// 6. Summary.
	generated := 0
	for _, r := range reports {
		if r.Error == "" {
			generated++
		}
	}
	fmt.Printf("\nDone: %d/%d tools generated in %s/\n", generated, len(apps), toolsDir)
	fmt.Printf("Report: %s\n", reportPath)
}

// appReport holds data for a single app's report entry.
type appReport struct {
	AppID       string
	Label       string
	Description string
	File        string
	Inputs      []inputReport
	Error       string
}

type inputReport struct {
	ID       string
	CWLType  string
	Required bool
	Default  string
	Desc     string
	BVBRCType string
	EnumVals []string
}

// fetchApps calls enumerate_apps and unwraps the [[...]] response.
func fetchApps(ctx context.Context, caller bvbrc.RPCCaller) ([]map[string]any, error) {
	result, err := caller.Call(ctx, "AppService.enumerate_apps", []any{})
	if err != nil {
		return nil, fmt.Errorf("enumerate_apps: %w", err)
	}

	var outer []json.RawMessage
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		return nil, fmt.Errorf("enumerate_apps: unexpected shape: %s", truncate(string(result), 200))
	}

	var apps []map[string]any
	if err := json.Unmarshal(outer[0], &apps); err != nil {
		return nil, fmt.Errorf("enumerate_apps: cannot parse app array: %v", err)
	}
	return apps, nil
}

// generateCWLTool creates a .cwl file for the given app and returns report data.
func generateCWLTool(app map[string]any, toolsDir string) appReport {
	appID, _ := app["id"].(string)
	label, _ := app["label"].(string)
	desc, _ := app["description"].(string)

	if appID == "" {
		return appReport{Error: "missing app id"}
	}

	report := appReport{
		AppID:       appID,
		Label:       label,
		Description: desc,
		File:        fmt.Sprintf("tools/%s.cwl", appID),
	}

	// Extract and map parameters.
	var inputs []inputReport
	if params, ok := app["parameters"].([]any); ok {
		for _, raw := range params {
			p, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := p["id"].(string)
			if id == "" {
				continue
			}

			bvbrcType, _ := p["type"].(string)
			required := isRequired(p["required"])
			cwlType := mapBVBRCType(bvbrcType, required)
			defVal := formatDefault(p["default"])
			paramDesc, _ := p["desc"].(string)
			if paramDesc == "" {
				paramDesc, _ = p["label"].(string)
			}

			var enumVals []string
			if ev, ok := p["enum"].([]any); ok {
				for _, v := range ev {
					if s, ok := v.(string); ok {
						enumVals = append(enumVals, s)
					}
				}
			}

			inputs = append(inputs, inputReport{
				ID:        id,
				CWLType:   cwlType,
				Required:  required,
				Default:   defVal,
				Desc:      paramDesc,
				BVBRCType: bvbrcType,
				EnumVals:  enumVals,
			})
		}
	}

	// Always include output_path and output_file if not already present.
	hasOutput := false
	hasOutputFile := false
	for _, inp := range inputs {
		if inp.ID == "output_path" {
			hasOutput = true
		}
		if inp.ID == "output_file" {
			hasOutputFile = true
		}
	}
	if !hasOutput {
		inputs = append(inputs, inputReport{
			ID: "output_path", CWLType: "Directory?", Required: false,
			Desc: "Workspace folder for results (framework parameter)", BVBRCType: "folder",
		})
	}
	if !hasOutputFile {
		inputs = append(inputs, inputReport{
			ID: "output_file", CWLType: "string", Required: true,
			Desc: "Prefix for output file names", BVBRCType: "string",
		})
	}

	report.Inputs = inputs

	// Build CWL content.
	cwl := buildCWL(appID, label, desc, inputs)

	// Write file.
	path := filepath.Join(toolsDir, appID+".cwl")
	if err := os.WriteFile(path, []byte(cwl), 0o644); err != nil {
		report.Error = fmt.Sprintf("write file: %v", err)
	}
	return report
}

// buildCWL constructs the CWL CommandLineTool YAML string.
func buildCWL(appID, label, desc string, inputs []inputReport) string {
	var b strings.Builder

	b.WriteString("cwlVersion: v1.2\nclass: CommandLineTool\n\n")

	// Doc line.
	docText := label
	if desc != "" && desc != label {
		docText = label + " — " + desc
	}
	if docText == "" {
		docText = appID
	}
	b.WriteString(fmt.Sprintf("doc: %q\n\n", docText))

	// Hints.
	b.WriteString("hints:\n")
	b.WriteString("  goweHint:\n")
	b.WriteString(fmt.Sprintf("    bvbrc_app_id: %s\n", appID))
	b.WriteString("    executor: bvbrc\n\n")

	// baseCommand = app ID.
	b.WriteString(fmt.Sprintf("baseCommand: [%s]\n\n", appID))

	// Inputs.
	b.WriteString("inputs:\n")
	for _, inp := range inputs {
		b.WriteString(fmt.Sprintf("  %s:\n", inp.ID))
		b.WriteString(fmt.Sprintf("    type: %s\n", inp.CWLType))
		if inp.Desc != "" {
			b.WriteString(fmt.Sprintf("    doc: %q\n", inp.Desc))
		}
		if inp.Default != "" {
			b.WriteString(fmt.Sprintf("    default: %s\n", inp.Default))
		}
	}

	// Outputs — generic Directory output.
	b.WriteString("\noutputs:\n")
	b.WriteString("  result:\n")
	b.WriteString("    type: Directory\n")
	b.WriteString("    outputBinding:\n")
	b.WriteString("      glob: \".\"\n")

	return b.String()
}

// mapBVBRCType converts a BV-BRC parameter type to a CWL type.
func mapBVBRCType(bvbrcType string, required bool) string {
	cwlType := "string"
	switch strings.ToLower(bvbrcType) {
	case "int", "integer":
		cwlType = "int"
	case "float", "number":
		cwlType = "float"
	case "boolean", "bool":
		cwlType = "boolean"
	case "folder":
		cwlType = "Directory"
	}
	if !required {
		cwlType += "?"
	}
	return cwlType
}

// isRequired determines if a BV-BRC parameter is required.
// Handles float64 (JSON number), bool, and string representations.
func isRequired(v any) bool {
	switch r := v.(type) {
	case float64:
		return r != 0
	case bool:
		return r
	case string:
		return r == "1" || strings.EqualFold(r, "true")
	default:
		return false
	}
}

// formatDefault returns a YAML-safe default value string, or empty if none.
func formatDefault(v any) string {
	if v == nil {
		return ""
	}
	switch d := v.(type) {
	case string:
		if d == "" {
			return ""
		}
		return fmt.Sprintf("%q", d)
	case float64:
		if d == float64(int(d)) {
			return fmt.Sprintf("%d", int(d))
		}
		return fmt.Sprintf("%g", d)
	case bool:
		if d {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// writeReport generates a markdown review report.
func writeReport(path string, reports []appReport) error {
	var b strings.Builder

	b.WriteString("# BV-BRC CWL Tools Report\n\n")
	b.WriteString(fmt.Sprintf("Generated: %s\n\n", time.Now().Format("2006-01-02")))

	// Summary.
	generated := 0
	for _, r := range reports {
		if r.Error == "" {
			generated++
		}
	}
	b.WriteString("## Summary\n\n")
	b.WriteString(fmt.Sprintf("- Total apps: %d\n", len(reports)))
	b.WriteString(fmt.Sprintf("- Tools generated: %d\n\n", generated))

	b.WriteString("---\n\n")

	// Per-app sections.
	for _, r := range reports {
		b.WriteString(fmt.Sprintf("## %s\n\n", r.AppID))

		if r.Error != "" {
			b.WriteString(fmt.Sprintf("**Error**: %s\n\n---\n\n", r.Error))
			continue
		}

		if r.Label != "" {
			b.WriteString(fmt.Sprintf("**Label**: %s\n", r.Label))
		}
		if r.Description != "" {
			b.WriteString(fmt.Sprintf("**Description**: %s\n", r.Description))
		}
		b.WriteString(fmt.Sprintf("**File**: `%s`\n\n", r.File))

		b.WriteString(fmt.Sprintf("### Inputs (%d parameters)\n\n", len(r.Inputs)))
		b.WriteString("| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |\n")
		b.WriteString("|-----------|----------|-------------|----------|---------|-------------|\n")
		for _, inp := range r.Inputs {
			req := "no"
			if inp.Required {
				req = "yes"
			}
			def := "—"
			if inp.Default != "" {
				def = inp.Default
			}
			descStr := inp.Desc
			if len(inp.EnumVals) > 0 {
				descStr += fmt.Sprintf(" [enum: %s]", strings.Join(inp.EnumVals, ", "))
			}
			bType := inp.BVBRCType
			if bType == "" {
				bType = "—"
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
				inp.ID, inp.CWLType, bType, req, def, descStr))
		}

		b.WriteString("\n### Outputs (guessed)\n\n")
		b.WriteString("| Output | CWL Type | Notes |\n")
		b.WriteString("|--------|----------|-------|\n")
		b.WriteString("| result | Directory | All BV-BRC output files |\n\n")

		b.WriteString("### Review Notes\n\n")
		b.WriteString("- [ ] Verify input types — complex params may need File or array types\n")
		b.WriteString("- [ ] Identify expected output files for specific glob patterns\n")
		b.WriteString("- [ ] Check default values are correct\n\n")

		b.WriteString("---\n\n")
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
