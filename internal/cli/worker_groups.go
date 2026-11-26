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

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/pkg/model"
	"github.com/spf13/cobra"
)

// Worker group manager commands: the users and the group-only worker keys of
// the worker groups the caller manages (server admins: every group with a
// rule). Managers are appointed by server admins (gowe admin worker-groups
// set --manager).

const managedGroupsAPIPath = "/api/v1/worker-groups"

// managedGroupPath is the API path of one group, plus optional segments,
// each escaped as one path segment ("/" included).
func managedGroupPath(group string, segments ...string) string {
	p := managedGroupsAPIPath + "/" + url.PathEscape(group)
	for _, s := range segments {
		p += "/" + url.PathEscape(s)
	}
	return p
}

func newWorkerGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker-groups",
		Short: "Manage the users and worker keys of the worker groups you manage",
		Long: `For managers of a worker group (appointed by a server admin) and for server
admins: show the groups you manage, change who may submit to them, and issue
or revoke worker keys valid for exactly one group. Starting a worker with such
a key (gowe-worker --group <group> --worker-key-file FILE) adds it to the
group.

Whoever holds a key of a group can run a worker that receives every task sent
to that group, including the submitter's data and the token used for staging.
Managers, the admins switch, default-deny and removing a rule stay with server
admins (gowe admin worker-groups).`,
	}
	cmd.AddCommand(
		newWorkerGroupsListCmd(),
		newWorkerGroupsShowCmd(),
		newWorkerGroupsAddUserCmd(),
		newWorkerGroupsRemoveUserCmd(),
		newWorkerGroupsKeysCmd(),
		newWorkerGroupsCreateKeyCmd(),
		newWorkerGroupsRevokeKeyCmd(),
	)
	return cmd
}

// printManagedGroup prints one rule.
func printManagedGroup(cmd *cobra.Command, rule *model.WorkerGroupPolicy) {
	out := cmd.OutOrStdout()
	admins := "no"
	if rule.Admins {
		admins = "yes"
	}
	fmt.Fprintf(out, "Group:    %s\n", rule.Group)
	fmt.Fprintf(out, "Users:    %s\n", orDefault(strings.Join(rule.Users, ", "), "(none)"))
	fmt.Fprintf(out, "Managers: %s\n", orDefault(strings.Join(rule.Managers, ", "), "(none)"))
	fmt.Fprintf(out, "Admins:   %s\n", admins)
	if !rule.UpdatedAt.IsZero() {
		fmt.Fprintf(out, "Updated:  %s by %s\n", rule.UpdatedAt.UTC().Format(time.RFC3339), orDash(rule.UpdatedBy))
	}
}

// parseManagedGroupRule decodes a rule from a response.
func parseManagedGroupRule(data json.RawMessage) (*model.WorkerGroupPolicy, error) {
	var rule model.WorkerGroupPolicy
	if err := json.Unmarshal(data, &rule); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &rule, nil
}

func newWorkerGroupsListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the worker groups you manage (server admins: every group with a rule)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			resp, err := client.Get(managedGroupsAPIPath)
			if err != nil {
				return err
			}
			if asJSON {
				return printRaw(out, resp.Data)
			}
			var body struct {
				Groups []model.WorkerGroupPolicy `json:"groups"`
			}
			if err := json.Unmarshal(resp.Data, &body); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			if len(body.Groups) == 0 {
				fmt.Fprintln(out, "You manage no worker group.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GROUP\tUSERS\tMANAGERS")
			for _, g := range body.Groups {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", g.Group, orDefault(strings.Join(g.Users, ","), "(none)"),
					orDefault(strings.Join(g.Managers, ","), "(none)"))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON")
	return cmd
}

func newWorkerGroupsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <group>",
		Short: "Show the rule of a worker group you manage",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			if err := groupacl.ValidateGroupName(group); err != nil {
				return err
			}
			resp, err := client.Get(managedGroupPath(group))
			if err != nil {
				return fmt.Errorf("show worker group %s: %w", group, err)
			}
			rule, err := parseManagedGroupRule(resp.Data)
			if err != nil {
				return err
			}
			printManagedGroup(cmd, rule)
			return nil
		},
	}
}

func newWorkerGroupsAddUserCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-user <group> <username>",
		Short: "Allow a user to submit to a worker group you manage",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			if err := groupacl.ValidateGroupName(group); err != nil {
				return err
			}
			names, err := groupacl.ValidateUsers([]string{args[1]})
			if err != nil {
				return err
			}
			resp, err := client.Post(managedGroupPath(group, "users"), map[string]any{"username": names[0]})
			if err != nil {
				return fmt.Errorf("add user to worker group %s: %w", group, err)
			}
			rule, err := parseManagedGroupRule(resp.Data)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Worker group %s users: %s\n", rule.Group, orDefault(strings.Join(rule.Users, ", "), "(none)"))
			return nil
		},
	}
}

func newWorkerGroupsRemoveUserCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-user <group> <username>",
		Short: "Stop a user from submitting to a worker group you manage",
		Long: `Removes a user from the users of a worker group. Tasks of the user that are
already QUEUED or RUNNING on the group are not stopped.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			group, username := args[0], args[1]
			if err := groupacl.ValidateGroupName(group); err != nil {
				return err
			}
			if username == "" {
				return errors.New("username is empty")
			}
			resp, err := client.Delete(managedGroupPath(group, "users", username), nil)
			if err != nil {
				return fmt.Errorf("remove user from worker group %s: %w", group, err)
			}
			rule, err := parseManagedGroupRule(resp.Data)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Worker group %s users: %s\n", rule.Group, orDefault(strings.Join(rule.Users, ", "), "(none)"))
			return nil
		},
	}
}

func newWorkerGroupsKeysCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "keys <group>",
		Short: "List the worker keys valid for exactly this group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			if err := groupacl.ValidateGroupName(group); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			resp, err := client.Get(managedGroupPath(group, "keys"))
			if err != nil {
				return fmt.Errorf("list worker keys of group %s: %w", group, err)
			}
			if asJSON {
				return printRaw(out, resp.Data)
			}
			var body struct {
				WorkerKeys []model.WorkerKey `json:"worker_keys"`
			}
			if err := json.Unmarshal(resp.Data, &body); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			if len(body.WorkerKeys) == 0 {
				fmt.Fprintf(out, "No worker keys for group %s.\n", group)
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tLABEL\tPREFIX\tDISABLED\tEXPIRES\tLAST USED\tCREATED BY")
			for _, k := range body.WorkerKeys {
				disabled := "no"
				if k.Disabled {
					disabled = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, orDash(k.Label), k.KeyPrefix, disabled,
					formatOptionalTime(k.ExpiresAt), formatOptionalTime(k.LastUsedAt), orDash(k.CreatedBy))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON")
	return cmd
}

// formatOptionalTime is t in RFC 3339 (UTC), "-" when nil.
func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}

// parseKeyExpiry parses --expires: an RFC 3339 time, or a positive duration
// (Go syntax, e.g. 720h) from now. "" is no expiry (nil).
func parseKeyExpiry(value string, now time.Time) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		t = t.UTC()
		return &t, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("--expires takes an RFC 3339 time (2027-01-01T00:00:00Z) or a positive duration (720h), not %q", value)
	}
	t := now.Add(d).UTC()
	return &t, nil
}

// writeKeyFile writes the raw key and a newline to f and closes it. A
// variable so tests can make it fail.
var writeKeyFile = func(f *os.File, key string) error {
	if _, err := f.WriteString(key + "\n"); err != nil {
		return err
	}
	return f.Close()
}

func newWorkerGroupsCreateKeyCmd() *cobra.Command {
	var (
		label       string
		description string
		expires     string
		outFile     string
	)
	cmd := &cobra.Command{
		Use:   "create-key <group>",
		Short: "Issue a worker key valid for exactly this group",
		Long: `Issues a worker key valid for exactly this group. A worker started with it
(gowe-worker --group <group> --worker-key-file FILE) joins the group and
receives every task sent to it, including the submitter's data and the token
used for staging.

The raw key is shown once: printed to stdout, or with --out written to a new
file with mode 0600 (an existing file is never overwritten) and not printed.
Revoking the key later (revoke-key) does not stop a task already running.
Refused until a server admin has created the first worker key (worker
authentication must be on).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			if err := groupacl.ValidateGroupName(group); err != nil {
				return err
			}
			if label == "" {
				return errors.New("--label is required")
			}
			expiresAt, err := parseKeyExpiry(expires, time.Now())
			if err != nil {
				return err
			}
			body := map[string]any{"label": label, "description": description}
			if expiresAt != nil {
				body["expires_at"] = expiresAt.Format(time.RFC3339)
			}

			// Create the output file before the key exists, so a file that
			// cannot be created costs no key; O_EXCL never overwrites.
			var f *os.File
			if outFile != "" {
				f, err = os.OpenFile(outFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return fmt.Errorf("--out: %w", err)
				}
			}
			// cleanup closes and removes the --out file (empty, or partly
			// written).
			cleanup := func() {
				if f != nil {
					f.Close()
					os.Remove(outFile)
				}
			}
			// mayExist is the note for a failure after which a key may
			// exist on the server without its raw value having been kept.
			mayExist := fmt.Sprintf("a worker key may have been created on the server: find it with `gowe worker-groups keys %s` and revoke it with `gowe worker-groups revoke-key %s <key-id>`",
				group, group)

			// PostSecret: the response holds the raw key, so it is never
			// logged (not even with --debug).
			resp, err := client.PostSecret(managedGroupPath(group, "keys"), body)
			if err != nil {
				cleanup()
				var apiErr *model.APIError
				if errors.As(err, &apiErr) {
					// The server answered with an error: no key.
					return fmt.Errorf("create worker key for group %s: %w", group, err)
				}
				return fmt.Errorf("create worker key for group %s: %w; %s", group, err, mayExist)
			}
			var minted struct {
				Key       string          `json:"key"`
				WorkerKey model.WorkerKey `json:"worker_key"`
			}
			if err := json.Unmarshal(resp.Data, &minted); err != nil || minted.Key == "" {
				cleanup()
				if err == nil {
					err = errors.New("no key in the response")
				}
				return fmt.Errorf("parse response: %w; %s", err, mayExist)
			}

			errOut := cmd.ErrOrStderr()
			if f != nil {
				if err := writeKeyFile(f, minted.Key); err != nil {
					cleanup()
					id := minted.WorkerKey.ID
					return fmt.Errorf("write %s: %w; the file was removed, but the worker key %s was created on the server: revoke it with `gowe worker-groups revoke-key %s %s` (`gowe worker-groups keys %s` lists the keys)",
						outFile, err, id, group, id, group)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Worker key %s for group %s written to %s (mode 0600)\n", minted.WorkerKey.ID, group, outFile)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), minted.Key)
			fmt.Fprintf(errOut, "Worker key %s for group %s. The key above is not shown again: store it now (a file with mode 0600, for gowe-worker --worker-key-file).\n",
				minted.WorkerKey.ID, group)
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "Label of the key (required)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the key")
	cmd.Flags().StringVar(&expires, "expires", "", "Expiry: an RFC 3339 time or a duration from now (e.g. 720h)")
	cmd.Flags().StringVar(&outFile, "out", "", "Write the raw key to this new file (mode 0600) instead of printing it")
	return cmd
}

func newWorkerGroupsRevokeKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke-key <group> <key-id>",
		Short: "Revoke (delete) a worker key valid for exactly this group",
		Long: `Deletes a worker key whose groups are exactly this group. Workers using it
are refused from then on; a task already running is not stopped and keeps the
token it received.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			group, id := args[0], args[1]
			if err := groupacl.ValidateGroupName(group); err != nil {
				return err
			}
			if id == "" {
				return errors.New("key id is empty")
			}
			if _, err := client.Delete(managedGroupPath(group, "keys", id), nil); err != nil {
				return fmt.Errorf("revoke worker key %s of group %s: %w", id, group, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked worker key %s of group %s\n", id, group)
			return nil
		},
	}
}
