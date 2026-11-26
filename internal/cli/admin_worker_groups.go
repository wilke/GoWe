package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/pkg/model"
	"github.com/spf13/cobra"
)

// Worker-group access policy commands (#261): which submitters may send
// tasks to a worker group. The rules are stored in the server's database and
// take effect at once for new submissions, dispatch and re-queue.

const workerGroupsAPIPath = "/api/v1/admin/worker-groups"

func newAdminWorkerGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker-groups",
		Short: "Show or change which submitters may use which worker groups",
		Long: `Manages the worker-group access policy stored in the server's database.
A group with a rule may be used only by the users it lists (and by server
admins when the rule says so). A group without a rule is open, unless
default-deny is on: then every group without a rule except the default group
is for server admins only. The default group is written "default".

A change takes effect at once for new submissions, for dispatch and for
re-queued tasks; tasks already QUEUED or RUNNING are not checked again.`,
	}
	cmd.AddCommand(
		newAdminWorkerGroupsListCmd(),
		newAdminWorkerGroupsSetCmd(),
		newAdminWorkerGroupsDeleteCmd(),
		newAdminWorkerGroupsDefaultDenyCmd(),
	)
	return cmd
}

func newAdminWorkerGroupsListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the worker-group rules and default-deny",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			resp, err := client.Get(workerGroupsAPIPath)
			if err != nil {
				return err
			}
			if asJSON {
				return printRaw(out, resp.Data)
			}
			var body struct {
				DefaultDeny bool                      `json:"default_deny"`
				Groups      []model.WorkerGroupPolicy `json:"groups"`
			}
			if err := json.Unmarshal(resp.Data, &body); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			if body.DefaultDeny {
				fmt.Fprintln(out, "default-deny: true (groups without a rule, except default, are for server admins only)")
			} else {
				fmt.Fprintln(out, "default-deny: false (groups without a rule are open)")
			}
			if len(body.Groups) == 0 {
				fmt.Fprintln(out, "No worker group rules.")
				return nil
			}
			fmt.Fprintln(out)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GROUP\tUSERS\tADMINS\tUPDATED BY\tUPDATED")
			for _, g := range body.Groups {
				admins := "no"
				if g.Admins {
					admins = "yes"
				}
				updated := "-"
				if !g.UpdatedAt.IsZero() {
					updated = g.UpdatedAt.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", g.Group, orDefault(strings.Join(g.Users, ","), "(none)"),
					admins, orDash(g.UpdatedBy), updated)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON")
	return cmd
}

func newAdminWorkerGroupsSetCmd() *cobra.Command {
	var (
		users  []string
		admins bool
	)
	cmd := &cobra.Command{
		Use:   "set <group>",
		Short: "Create or replace the rule of a worker group",
		Long: `Creates or replaces the rule of a worker group: only the users given with
--user (repeatable, exact and case-sensitive usernames) may use it, and server
admins too with --admins.

set replaces the whole rule, it does not edit it: omitting --admins turns the
admins switch off, and omitting --user leaves nobody in the users list. With no
--user and no --admins nobody may use the group.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			if err := groupacl.ValidateGroupName(group); err != nil {
				return err
			}
			list, err := groupacl.ValidateUsers(users)
			if err != nil {
				return fmt.Errorf("--user: %w", err)
			}
			resp, err := client.Put(workerGroupsAPIPath+"/"+url.PathEscape(group), map[string]any{"users": list, "admins": admins})
			if err != nil {
				return fmt.Errorf("set worker group %s: %w", group, err)
			}
			var rule model.WorkerGroupPolicy
			if err := json.Unmarshal(resp.Data, &rule); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			out := cmd.OutOrStdout()
			who := orDefault(strings.Join(rule.Users, ", "), "(no users)")
			if rule.Admins {
				who += " and server admins"
			}
			fmt.Fprintf(out, "Worker group %s may be used by: %s\n", rule.Group, who)
			if len(rule.Users) == 0 && !rule.Admins {
				fmt.Fprintln(out, "Nobody may use this group now.")
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&users, "user", nil, "Username allowed to use the group (repeatable)")
	cmd.Flags().BoolVar(&admins, "admins", false, "Also allow server admins")
	return cmd
}

func newAdminWorkerGroupsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <group>",
		Short: "Remove the rule of a worker group (it becomes open, or admin-only under default-deny)",
		Long: `Removes the rule of a worker group: it becomes open, or admin-only under
default-deny. The name is not checked against the group name rule, so a stored
rule with an invalid name (which keeps the server from reloading the policy)
can be removed too.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			group := args[0]
			if group == "" {
				return errors.New(`group name is empty (the default group is "default")`)
			}
			if _, err := client.Delete(workerGroupsAPIPath+"/"+url.PathEscape(group), nil); err != nil {
				return fmt.Errorf("delete worker group %s: %w", group, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed the rule of worker group %s\n", group)
			return nil
		},
	}
}

func newAdminWorkerGroupsDefaultDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default-deny <true|false>",
		Short: "Make groups without a rule (except default) admin-only, or open again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deny, err := groupacl.ParseDefaultDeny(args[0])
			if err != nil {
				return fmt.Errorf("default-deny takes true or false, not %q", args[0])
			}
			if _, err := client.Put(workerGroupsAPIPath+"-default-deny", map[string]any{"default_deny": deny}); err != nil {
				return fmt.Errorf("set default-deny: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "default-deny set to %v\n", deny)
			return nil
		},
	}
}
