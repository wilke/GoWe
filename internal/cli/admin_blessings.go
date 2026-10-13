package cli

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/me/gowe/pkg/model"
	"github.com/spf13/cobra"
)

// Tool blessing commands: review a tool, pin its image digests and record an
// administrator's approval for it to receive a user's token. Nothing enforces
// blessings yet; these commands manage the records.

// inspectBody mirrors the response of POST /admin/tool-blessings/inspect.
type inspectBody struct {
	ToolID         string              `json:"tool_id"`
	ToolHash       string              `json:"tool_hash"`
	ImageRef       string              `json:"image_ref"`
	RequestsToken  bool                `json:"requests_token"`
	WorkflowID     string              `json:"workflow_id"`
	WorkflowName   string              `json:"workflow_name"`
	Summary        model.ToolSummary   `json:"summary"`
	Warnings       []string            `json:"warnings"`
	ActiveBlessing *model.ToolBlessing `json:"active_blessing"`
}

// sha256File returns the hex sha256 of a file's contents.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// collectImageDigests merges --image-file and --image-sha256 into a de-duplicated,
// validated list of lowercase hex digests.
func collectImageDigests(w io.Writer, files, digests []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, f := range files {
		d, err := sha256File(f)
		if err != nil {
			return nil, fmt.Errorf("--image-file: %w", err)
		}
		fmt.Fprintf(w, "image sha256 %s  (%s)\n", d, f)
		add(d)
	}
	for _, d := range digests {
		if !model.IsValidSHA256(d) {
			return nil, fmt.Errorf("--image-sha256 %q is not a 64-character hex digest", d)
		}
		add(model.NormalizeSHA256(d))
	}
	return out, nil
}

// printInspectReport writes the review report. Every IWDR script is printed
// verbatim; review flags are printed prominently.
func printInspectReport(w io.Writer, in *inspectBody) {
	s := in.Summary
	fmt.Fprintf(w, "Tool:        %s\n", in.ToolID)
	if in.WorkflowName != "" {
		fmt.Fprintf(w, "Registered:  %s (%s)\n", in.WorkflowName, in.WorkflowID)
	}
	fmt.Fprintf(w, "Hash:        %s\n", in.ToolHash)
	fmt.Fprintf(w, "Image ref:   %s\n", orDash(in.ImageRef))
	fmt.Fprintf(w, "Requests the token (gowe:Execution.inject_bvbrc_token): %v\n", in.RequestsToken)
	if len(s.BaseCommand) > 0 {
		fmt.Fprintf(w, "baseCommand: %s\n", strings.Join(s.BaseCommand, " "))
	}
	for _, a := range s.Arguments {
		fmt.Fprintf(w, "argument:    %s\n", a)
	}
	if len(s.RequirementClasses) > 0 {
		fmt.Fprintf(w, "requirements: %s\n", strings.Join(s.RequirementClasses, ", "))
	}
	if len(s.HintClasses) > 0 {
		fmt.Fprintf(w, "hints:        %s\n", strings.Join(s.HintClasses, ", "))
	}
	for _, e := range s.EnvVars {
		fmt.Fprintf(w, "env:         %s\n", e)
	}
	fmt.Fprintf(w, "inputs:      %s\n", strings.Join(s.Inputs, ", "))
	fmt.Fprintf(w, "outputs:     %s\n", strings.Join(s.Outputs, ", "))
	for _, sc := range s.Scripts {
		fmt.Fprintf(w, "\n----- script: %s -----\n%s", sc.Source, sc.Content)
		if !strings.HasSuffix(sc.Content, "\n") {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "----- end script -----")
	}
	for _, warn := range in.Warnings {
		fmt.Fprintf(w, "\nWARNING: %s\n", warn)
	}
	if len(s.Flags) > 0 {
		fmt.Fprintf(w, "\nREVIEW FLAGS (%d) - confirm each is acceptable before blessing:\n", len(s.Flags))
		for _, f := range s.Flags {
			fmt.Fprintf(w, "  !! %s\n", f)
		}
	} else {
		fmt.Fprintln(w, "\nReview flags: none")
	}
	fmt.Fprintln(w, "\nReview checklist: read every script above; confirm no output (declared stdout/stderr, logs, files) can carry the token;")
	fmt.Fprintln(w, "stderr is where tracebacks and HTTP error bodies land. The token has no audience claim and cannot be revoked upstream.")
	if in.ActiveBlessing != nil {
		fmt.Fprintf(w, "\nNOTE: an active blessing already exists for this hash: %s (images: %s)\n",
			in.ActiveBlessing.ID, strings.Join(in.ActiveBlessing.ImageSHA256s, ", "))
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// confirm asks a yes/no question on in/out; "yes" (any case, or "y") confirms.
func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [yes/no]: ", prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "yes", "y":
		return true
	}
	return false
}

func newAdminBlessCmd() *cobra.Command {
	var (
		toolID     string
		imageFiles []string
		imageSHAs  []string
		envNames   []string
		groups     []string
		note       string
		yes        bool
	)
	cmd := &cobra.Command{
		Use:   "bless <workflow-name|wf_id|file.cwl>",
		Short: "Review a tool and bless it to receive the user's token",
		Long: `Prints the review report for a tool (hash, image, every InitialWorkDir script
verbatim, and review flags for output handling), then asks for confirmation and
records the blessing.

A blessing pins the image: at least one of --image-file (a .sif; its sha256 is
computed locally) or --image-sha256 is required. Add digests for other host
builds later with 'gowe admin blessings add-image'. Blessings do not expire;
revoke with 'gowe admin unbless'.

If the argument is a local CWL file it is only inspected; the tool must be
registered (the server matches the hash) to be blessed. This change does not
yet enforce blessings anywhere.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(imageFiles) == 0 && len(imageSHAs) == 0 {
				return errors.New("a blessing pins its image: give --image-file <sif> and/or --image-sha256 <hex>")
			}
			digests, err := collectImageDigests(out, imageFiles, imageSHAs)
			if err != nil {
				return err
			}
			for _, n := range envNames {
				if n == "" {
					return errors.New("--env: empty name")
				}
			}

			inspectReq := map[string]any{}
			createReq := map[string]any{}
			if st, err := os.Stat(args[0]); err == nil && !st.IsDir() {
				data, err := os.ReadFile(args[0])
				if err != nil {
					return err
				}
				inspectReq["cwl"] = string(data)
			} else {
				inspectReq["workflow"] = args[0]
				createReq["workflow"] = args[0]
			}
			if toolID != "" {
				inspectReq["tool_id"] = toolID
				createReq["tool_id"] = toolID
			}

			resp, err := client.Post("/api/v1/admin/tool-blessings/inspect", inspectReq)
			if err != nil {
				return fmt.Errorf("inspect: %w", err)
			}
			var rep inspectBody
			if err := json.Unmarshal(resp.Data, &rep); err != nil {
				return fmt.Errorf("parse inspect response: %w", err)
			}
			printInspectReport(out, &rep)

			createReq["tool_hash"] = rep.ToolHash
			createReq["image_sha256s"] = digests
			if len(envNames) > 0 {
				createReq["env_names"] = envNames
			}
			if len(groups) > 0 {
				createReq["worker_groups"] = groups
			}
			if note != "" {
				createReq["note"] = note
			}

			fmt.Fprintf(out, "\nAbout to bless %s\n  image ref:     %s\n  image sha256s: %s\n  env names:     %s\n  worker groups: %s\n",
				rep.ToolHash, orDash(rep.ImageRef), strings.Join(digests, ", "),
				orDefault(strings.Join(envNames, ", "), "(default: "+strings.Join(model.DefaultTokenEnvNames, ", ")+")"),
				orDefault(strings.Join(groups, ", "), "(any)"))
			if !yes && !confirm(cmd.InOrStdin(), out, "I have read the scripts and flags above and approve this tool receiving the user's token") {
				return errors.New("aborted: not blessed")
			}

			resp, err = client.Post("/api/v1/admin/tool-blessings", createReq)
			if err != nil {
				return fmt.Errorf("bless: %w", err)
			}
			var b model.ToolBlessing
			if err := json.Unmarshal(resp.Data, &b); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			fmt.Fprintf(out, "Blessed %s as %s\n", b.ToolHash, b.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&toolID, "tool", "", "Tool id within the document (required when it has several tools)")
	cmd.Flags().StringArrayVar(&imageFiles, "image-file", nil, "Path to the .sif image; its sha256 is computed locally and pinned (repeatable)")
	cmd.Flags().StringArrayVar(&imageSHAs, "image-sha256", nil, "Image sha256 digest (hex) to pin (repeatable)")
	cmd.Flags().StringSliceVar(&envNames, "env", nil, "Environment variable names the token is injected under (default BVBRC_TOKEN,KB_AUTH_TOKEN)")
	cmd.Flags().StringSliceVar(&groups, "groups", nil, "Restrict to these worker groups (default: any)")
	cmd.Flags().StringVar(&note, "note", "", "Free-text note recorded with the blessing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt (the report is still printed)")
	return cmd
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func newAdminBlessingsCmd() *cobra.Command {
	var (
		all     bool
		pending bool
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "blessings",
		Short: "List tool blessings (or tools awaiting one with --pending)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if pending {
				resp, err := client.Get("/api/v1/admin/tool-blessings/pending")
				if err != nil {
					return err
				}
				if asJSON {
					return printRaw(out, resp.Data)
				}
				var body struct {
					Pending []struct {
						ToolID       string `json:"tool_id"`
						ToolHash     string `json:"tool_hash"`
						ImageRef     string `json:"image_ref"`
						WorkflowName string `json:"workflow_name"`
					} `json:"pending"`
				}
				if err := json.Unmarshal(resp.Data, &body); err != nil {
					return err
				}
				if len(body.Pending) == 0 {
					fmt.Fprintln(out, "No token-requesting tools are awaiting a blessing.")
					return nil
				}
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "WORKFLOW\tTOOL\tIMAGE\tHASH")
				for _, p := range body.Pending {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.WorkflowName, p.ToolID, orDash(p.ImageRef), p.ToolHash)
				}
				return tw.Flush()
			}
			q := url.Values{}
			if all {
				q.Set("include_revoked", "true")
			}
			resp, err := client.Get("/api/v1/admin/tool-blessings?" + q.Encode())
			if err != nil {
				return err
			}
			if asJSON {
				return printRaw(out, resp.Data)
			}
			var body struct {
				Blessings []model.ToolBlessing `json:"blessings"`
			}
			if err := json.Unmarshal(resp.Data, &body); err != nil {
				return err
			}
			if len(body.Blessings) == 0 {
				fmt.Fprintln(out, "No blessings.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTOOL\tSTATE\tIMAGES\tGROUPS\tBLESSED BY\tHASH")
			for _, b := range body.Blessings {
				state := "active"
				if !b.Active() {
					state = "revoked: " + b.RevokeReason
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", b.ID, b.ToolName, state, len(b.ImageSHA256s),
					orDefault(strings.Join(b.WorkerGroups, ","), "any"), b.BlessedBy, b.ToolHash)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include revoked blessings")
	cmd.Flags().BoolVar(&pending, "pending", false, "List registered tools that request the token but are not blessed")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON")
	cmd.AddCommand(newAdminBlessingsAddImageCmd())
	return cmd
}

func printRaw(w io.Writer, data json.RawMessage) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newAdminBlessingsAddImageCmd() *cobra.Command {
	var (
		imageFiles []string
		imageSHAs  []string
	)
	cmd := &cobra.Command{
		Use:   "add-image <blessing-id|tool-hash>",
		Short: "Allow another image build (sha256) for a blessed tool",
		Long: `The same recipe built on another host yields a different image digest. Add
each host build's digest to the blessing; workers refuse to inject the token
into an image whose digest is not listed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(imageFiles) == 0 && len(imageSHAs) == 0 {
				return errors.New("give --image-file <sif> and/or --image-sha256 <hex>")
			}
			digests, err := collectImageDigests(out, imageFiles, imageSHAs)
			if err != nil {
				return err
			}
			for _, d := range digests {
				if _, err := client.Post("/api/v1/admin/tool-blessings/"+url.PathEscape(args[0])+"/images",
					map[string]any{"image_sha256": d}); err != nil {
					return fmt.Errorf("add image %s: %w", d, err)
				}
				fmt.Fprintf(out, "Added image sha256 %s to %s\n", d, args[0])
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&imageFiles, "image-file", nil, "Path to the .sif image; its sha256 is computed locally (repeatable)")
	cmd.Flags().StringArrayVar(&imageSHAs, "image-sha256", nil, "Image sha256 digest (hex) (repeatable)")
	return cmd
}

func newAdminUnblessCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "unbless <blessing-id|tool-hash>",
		Short: "Revoke a tool blessing (the record is kept)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return errors.New("--reason is required")
			}
			if _, err := client.Delete("/api/v1/admin/tool-blessings/"+url.PathEscape(args[0]), map[string]any{"reason": reason}); err != nil {
				return fmt.Errorf("unbless: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why the blessing is revoked (required)")
	return cmd
}
