package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// outputFileRow mirrors the per-file entry of the admin output endpoints.
type outputFileRow struct {
	SubmissionID     string `json:"submission_id"`
	Location         string `json:"location"`
	ExpectedChecksum string `json:"expected_checksum"`
	ActualChecksum   string `json:"actual_checksum"`
	ExpectedSize     int64  `json:"expected_size"`
	ActualSize       int64  `json:"actual_size"`
	OK               bool   `json:"ok"`
	Error            string `json:"error"`
	Action           string `json:"action"`
	Source           string `json:"source"`
}

// outputReportBody mirrors the response of verify-outputs and redeliver.
type outputReportBody struct {
	SubmissionID  string          `json:"submission_id"`
	State         string          `json:"state"`
	OutputState   string          `json:"output_state"`
	DryRun        bool            `json:"dry_run"`
	Submissions   []string        `json:"submissions"`
	Files         []outputFileRow `json:"files"`
	Updated       bool            `json:"updated"`
	StateRestored bool            `json:"state_restored"`
	Summary       struct {
		Total           int `json:"total"`
		OK              int `json:"ok"`
		Mismatched      int `json:"mismatched"`
		Errors          int `json:"errors"`
		Reuploaded      int `json:"reuploaded"`
		WouldReupload   int `json:"would_reupload"`
		OriginalMissing int `json:"original_missing"`
		Failed          int `json:"failed"`
	} `json:"summary"`
}

func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Administrative operations (admin role required)",
	}
	cmd.AddCommand(
		newAdminVerifyOutputsCmd(),
		newAdminRedeliverCmd(),
	)
	return cmd
}

// validateAdminVerifyArgs enforces "exactly one submission ID, or --all".
func validateAdminVerifyArgs(args []string, all bool) error {
	switch {
	case all && len(args) > 0:
		return errors.New("--all cannot be combined with a submission ID")
	case !all && len(args) != 1:
		return errors.New("requires exactly one submission ID (or --all)")
	}
	return nil
}

func newAdminVerifyOutputsCmd() *cobra.Command {
	var (
		all         bool
		outputState string
		since       string
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use:   "verify-outputs [submission_id]",
		Short: "Verify delivered workspace outputs against their recorded checksums",
		Long: `Downloads every ws:// output of a submission (including sub-workflow
children) and compares it to the checksum and size the worker recorded before
upload. Read-only. With --all, every submission whose output_state matches
--output-state (default: delivered,upload_failed) is verified in turn.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateAdminVerifyArgs(args, all); err != nil {
				return err
			}

			var ids []string
			if all {
				var err error
				ids, err = listSubmissionIDs(outputState, since)
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					fmt.Println("No submissions match the filter.")
					return nil
				}
			} else {
				ids = args
			}

			var reports []outputReportBody
			exitErr := false
			for _, id := range ids {
				report, err := postOutputReport("/api/v1/admin/submissions/" + id + "/verify-outputs")
				if err != nil {
					exitErr = true
					if asJSON {
						reports = append(reports, outputReportBody{SubmissionID: id, Files: []outputFileRow{{SubmissionID: id, Error: err.Error()}}})
					} else {
						fmt.Fprintf(os.Stderr, "%s: %v\n", id, err)
					}
					continue
				}
				if report.Summary.OK != report.Summary.Total {
					exitErr = true
				}
				reports = append(reports, *report)
			}

			if asJSON {
				return printJSON(reports)
			}
			printOutputReports(reports, false)
			if exitErr {
				return errors.New("one or more outputs failed verification")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Verify every submission matching --output-state/--since")
	cmd.Flags().StringVar(&outputState, "output-state", "delivered,upload_failed", "Comma-separated output states to select with --all")
	cmd.Flags().StringVar(&since, "since", "", "Only submissions created on/after this date (YYYY-MM-DD) with --all")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON reports")
	return cmd
}

func newAdminRedeliverCmd() *cobra.Command {
	var (
		dryRun bool
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "redeliver <submission_id>",
		Short: "Re-upload outputs that fail verification from their local originals",
		Long: `Verifies every output of the submission (including sub-workflow children),
locates the local original of each failing file by checksum and size in the
task outputs, re-uploads it to the same workspace path using the submission's
stored token, re-verifies it, and marks the submission delivered once every
output verifies. Nothing is ever deleted. Use --dry-run to see what would be
re-uploaded without changing anything.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/admin/submissions/" + args[0] + "/redeliver"
			if dryRun {
				path += "?dry_run=true"
			}
			report, err := postOutputReport(path)
			if err != nil {
				return fmt.Errorf("redeliver %s: %w", args[0], err)
			}
			if asJSON {
				return printJSON(report)
			}
			printOutputReports([]outputReportBody{*report}, true)
			if report.Summary.OK != report.Summary.Total {
				return errors.New("one or more outputs could not be re-delivered")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be re-uploaded without uploading or updating anything")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON report")
	return cmd
}

// listSubmissionIDs pages through /submissions with the output_state filter
// and returns every matching root submission ID.
func listSubmissionIDs(outputState, since string) ([]string, error) {
	var ids []string
	const pageSize = 100
	for offset := 0; ; offset += pageSize {
		q := url.Values{}
		q.Set("limit", fmt.Sprint(pageSize))
		q.Set("offset", fmt.Sprint(offset))
		if outputState != "" {
			q.Set("output_state", outputState)
		}
		if since != "" {
			q.Set("date_start", since)
		}
		resp, err := client.Get("/api/v1/submissions/?" + q.Encode())
		if err != nil {
			return nil, fmt.Errorf("list submissions: %w", err)
		}
		var page []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(resp.Data, &page); err != nil {
			return nil, fmt.Errorf("parse submissions: %w", err)
		}
		for _, s := range page {
			ids = append(ids, s.ID)
		}
		if resp.Pagination == nil || !resp.Pagination.HasMore || len(page) == 0 {
			break
		}
	}
	return ids, nil
}

// adminRequestTimeout bounds one verify/redeliver call. The server downloads
// (and may re-upload) every output before answering, so this is deliberately
// generous; it only exists so a dead connection cannot hang the CLI forever.
const adminRequestTimeout = 4 * time.Hour

func postOutputReport(path string) (*outputReportBody, error) {
	if client.HTTPClient.Timeout == 0 {
		client.HTTPClient.Timeout = adminRequestTimeout
	}
	resp, err := client.Post(path, nil)
	if err != nil {
		return nil, err
	}
	var report outputReportBody
	if err := json.Unmarshal(resp.Data, &report); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &report, nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printOutputReports renders one compact table across all reports, followed
// by a per-submission summary line.
func printOutputReports(reports []outputReportBody, withAction bool) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if withAction {
		fmt.Fprintln(tw, "SUBMISSION\tLOCATION\tEXPECTED\tACTUAL\tOK\tACTION\tERROR")
	} else {
		fmt.Fprintln(tw, "SUBMISSION\tLOCATION\tEXPECTED\tACTUAL\tOK\tERROR")
	}
	for _, r := range reports {
		for _, f := range r.Files {
			ok := "no"
			if f.OK {
				ok = "yes"
			}
			if withAction {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					f.SubmissionID, f.Location, shortChecksum(f.ExpectedChecksum), shortChecksum(f.ActualChecksum), ok, f.Action, f.Error)
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					f.SubmissionID, f.Location, shortChecksum(f.ExpectedChecksum), shortChecksum(f.ActualChecksum), ok, f.Error)
			}
		}
	}
	tw.Flush()

	fmt.Println()
	for _, r := range reports {
		if r.SubmissionID == "" {
			continue
		}
		s := r.Summary
		line := fmt.Sprintf("%s: %d/%d ok", r.SubmissionID, s.OK, s.Total)
		if s.Mismatched > 0 {
			line += fmt.Sprintf(", %d mismatched", s.Mismatched)
		}
		if s.Errors > 0 {
			line += fmt.Sprintf(", %d errors", s.Errors)
		}
		if withAction {
			if s.Reuploaded > 0 {
				line += fmt.Sprintf(", %d re-uploaded", s.Reuploaded)
			}
			if s.WouldReupload > 0 {
				line += fmt.Sprintf(", %d would re-upload", s.WouldReupload)
			}
			if s.OriginalMissing > 0 {
				line += fmt.Sprintf(", %d original missing", s.OriginalMissing)
			}
			if s.Failed > 0 {
				line += fmt.Sprintf(", %d failed", s.Failed)
			}
			if r.DryRun {
				line += " (dry run)"
			}
		}
		if r.OutputState != "" || r.State != "" {
			line += fmt.Sprintf(" [state=%s output_state=%s]", r.State, r.OutputState)
		}
		if r.StateRestored {
			line += " state restored to COMPLETED"
		}
		if len(r.Submissions) > 1 {
			line += fmt.Sprintf(" (%d submissions incl. children)", len(r.Submissions))
		}
		fmt.Println(line)
	}
}

// shortChecksum abbreviates "sha1$<40 hex>" to "sha1$<12 hex>" for the table.
func shortChecksum(sum string) string {
	if sum == "" {
		return "-"
	}
	algo, hexPart, ok := strings.Cut(sum, "$")
	if !ok || len(hexPart) <= 12 {
		return sum
	}
	return algo + "$" + hexPart[:12]
}
