package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/me/gowe/pkg/model"
	"github.com/spf13/cobra"
)

// Service token commands (admin only): identities GoWe issues to programs
// such as the model router.

const serviceTokensAPIPath = "/api/v1/admin/service-tokens"

func newAdminServiceTokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service-tokens",
		Short: "Manage service tokens (identities of programs such as the model router)",
		Long: `A service token gives a program its own identity: a request made with a
token of name N is the user "N@service.gowe", role user, never an admin and
never a worker group manager. It has no BV-BRC token, so it gets no Workspace
staging, and it can never use the BV-BRC executor: a workflow with a step on
it is refused (403 at submission and retry, the step fails at dispatch), and
the server's own BV-BRC account is never used for it. Name it in the users of
a worker group rule to let it submit there:

  gowe admin worker-groups set model-router --user model-router@service.gowe

Whoever holds the token is that user and can:
  - submit to every open worker group and to restricted groups that list it;
    it is not bound by --anonymous-executors, so it can use every executor
    but bvbrc, including local, which runs tools on the server host;
  - attach secrets to its own submissions, read, cancel, retry and delete
    them;
  - register workflows; update or delete its own and those with no owner;
    list and read every workflow (CWL included);
  - upload files (POST /files, with the server's upload credentials) and
    download any file under the allowed download directories, with no owner
    check;
  - read /apps (the server's BV-BRC account), /fleet and /hosts (hostnames
    redacted, worker names shown).
It cannot read, cancel or delete other users' submissions or their event
streams, see workers' peer addresses, be an admin or a group manager, or use
the web UI. Known gap: a task id of another user's submission (shown on
/fleet) is enough to read that task and its logs.

Issue one token per program, name it only in the groups it needs, set an
expiry (--expires) where the program can be re-keyed, and revoke it on
suspicion. Revoking every token of a name keeps its user row, rules and
submissions: a new token with that name inherits them. The program uses the
token with gowe --token-file FILE (a regular file, not a symlink, owned by
the user, mode 0600 or stricter, at most 4096 bytes) or in the
Authorization: Bearer header.`,
	}
	cmd.AddCommand(
		newAdminServiceTokensCreateCmd(),
		newAdminServiceTokensListCmd(),
		newAdminServiceTokensRevokeCmd(),
	)
	return cmd
}

// writeSecretFile writes secret and a newline to f, flushes it to stable
// storage and closes it. A variable so tests can make it fail.
var writeSecretFile = func(f *os.File, secret string) error {
	if _, err := f.WriteString(secret + "\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// createdServiceToken is the answer of POST /admin/service-tokens.
type createdServiceToken struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Username    string     `json:"username"`
	Token       string     `json:"token"`
	TokenPrefix string     `json:"token_prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

func newAdminServiceTokensCreateCmd() *cobra.Command {
	var (
		description string
		expires     string
		outFile     string
	)
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a service token; the token is written only to --out",
		Long: `Creates a service token for the service <name> (1-64 characters: a lower-case
letter or digit, then lower-case letters, digits, '.', '_' or '-'). Several
tokens may exist for one name (rotation).

The token is written only to the --out file, which must not exist: it is
created with mode 0600 and never overwritten. The token is never printed, also
not with --debug. Printed are the token id, the username to put into a worker
group rule, the prefix, the expiry and the file.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := model.ValidateServiceName(name); err != nil {
				return err
			}
			if outFile == "" {
				return errors.New("--out is required: the token is written only to a new file (mode 0600)")
			}
			body := map[string]any{"name": name, "description": description}
			if expires != "" {
				if d, err := time.ParseDuration(expires); err != nil || d <= 0 {
					return fmt.Errorf("--expires takes a positive duration from now (e.g. 2160h), not %q", expires)
				}
				body["expires_in"] = expires
			}

			// Create the output file before the token exists, so a file
			// that cannot be created costs no token; O_EXCL never
			// overwrites.
			f, err := os.OpenFile(outFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return fmt.Errorf("--out: %w", err)
			}
			// cleanup closes and removes the --out file (empty, or partly
			// written).
			cleanup := func() {
				f.Close()
				os.Remove(outFile)
			}
			mayExist := "a service token may have been created on the server: find it with `gowe admin service-tokens list` and revoke it with `gowe admin service-tokens revoke <id>`"

			// PostSecret: the response holds the token, so it is never
			// logged (not even with --debug) nor quoted in an error.
			resp, err := client.PostSecret(serviceTokensAPIPath, body)
			if err != nil {
				cleanup()
				var apiErr *model.APIError
				if errors.As(err, &apiErr) {
					// The server answered with an error: no token.
					return fmt.Errorf("create service token for %s: %w", name, err)
				}
				return fmt.Errorf("create service token for %s: %w; %s", name, err, mayExist)
			}
			var created createdServiceToken
			if err := json.Unmarshal(resp.Data, &created); err != nil || created.Token == "" {
				cleanup()
				if err == nil {
					err = errors.New("no token in the response")
				}
				return fmt.Errorf("parse response: %w; %s", err, mayExist)
			}
			if err := writeSecretFile(f, created.Token); err != nil {
				cleanup()
				return fmt.Errorf("write %s: %w; the file was removed, but the service token %s was created on the server: revoke it with `gowe admin service-tokens revoke %s`",
					outFile, err, created.ID, created.ID)
			}

			expiry := "never"
			if created.ExpiresAt != nil {
				expiry = created.ExpiresAt.UTC().Format(time.RFC3339)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Service token %s written to %s (mode 0600)\n", created.ID, outFile)
			fmt.Fprintf(out, "Username: %s\n", created.Username)
			fmt.Fprintf(out, "Prefix:   %s\n", created.TokenPrefix)
			fmt.Fprintf(out, "Expires:  %s\n", expiry)
			fmt.Fprintf(out, "Allow it in a worker group: gowe admin worker-groups set <group> --user %s\n", created.Username)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "Description of the token")
	cmd.Flags().StringVar(&expires, "expires", "", "Lifetime from now, a duration between 1h and 8760h (e.g. 2160h); default: no expiry")
	cmd.Flags().StringVar(&outFile, "out", "", "Write the token to this new file (mode 0600); required")
	return cmd
}

// serviceTokenRow is a token as GET /admin/service-tokens lists it.
type serviceTokenRow struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Username    string     `json:"username"`
	TokenPrefix string     `json:"token_prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	Expired     bool       `json:"expired"`
}

func newAdminServiceTokensListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the service tokens (never the tokens themselves)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			resp, err := client.Get(serviceTokensAPIPath)
			if err != nil {
				return fmt.Errorf("list service tokens: %w", err)
			}
			if asJSON {
				return printRaw(out, resp.Data)
			}
			var body struct {
				ServiceTokens []serviceTokenRow `json:"service_tokens"`
			}
			if err := json.Unmarshal(resp.Data, &body); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}
			if len(body.ServiceTokens) == 0 {
				fmt.Fprintln(out, "No service tokens.")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tUSERNAME\tPREFIX\tCREATED\tEXPIRES\tLAST USED")
			for _, t := range body.ServiceTokens {
				expires := "never"
				if t.ExpiresAt != nil {
					expires = t.ExpiresAt.UTC().Format(time.RFC3339)
					if t.Expired {
						expires += " (expired)"
					}
				}
				created := "-"
				if !t.CreatedAt.IsZero() {
					created = t.CreatedAt.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", terminalSafe(t.ID), terminalSafe(t.Name),
					terminalSafe(t.Username), terminalSafe(t.TokenPrefix), created, expires, formatOptionalTime(t.LastUsedAt))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON")
	return cmd
}

func newAdminServiceTokensRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke (delete) a service token",
		Long: `Deletes a service token. The next request made with it is refused. Work it
already submitted is not cancelled.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if id == "" {
				return errors.New("token id is empty")
			}
			if _, err := client.Delete(serviceTokensAPIPath+"/"+url.PathEscape(id), nil); err != nil {
				return fmt.Errorf("revoke service token %s: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked service token %s\n", id)
			return nil
		},
	}
}
