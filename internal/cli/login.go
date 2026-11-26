package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const credentialsFileName = "credentials.json"

type credentials struct {
	Token string `json:"token"`
}

func newLoginCmd() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with BV-BRC",
		Long:  "Store a BV-BRC authentication token for use with GoWe API calls.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				fmt.Print("BV-BRC token: ")
				reader := bufio.NewReader(os.Stdin)
				line, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("read token: %w", err)
				}
				token = strings.TrimSpace(line)
			}

			if token == "" {
				return fmt.Errorf("token cannot be empty")
			}

			credPath, err := credentialsPath()
			if err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(credPath), 0700); err != nil {
				return fmt.Errorf("create config directory: %w", err)
			}

			creds := credentials{Token: token}
			data, err := json.MarshalIndent(creds, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal credentials: %w", err)
			}

			if err := os.WriteFile(credPath, data, 0600); err != nil {
				return fmt.Errorf("write credentials: %w", err)
			}

			fmt.Printf("Credentials saved to %s\n", credPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "BV-BRC authentication token (prompted if omitted)")
	return cmd
}

// credentialsPath returns the path to the credentials file (~/.gowe/credentials.json).
func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".gowe", credentialsFileName), nil
}

// maxTokenFileBytes is the largest --token-file readTokenFile accepts.
const maxTokenFileBytes = 4096

// resolveToken is the CLI's token resolution: the --token-file FILE when
// given (it takes precedence; an unreadable or unsafe file is an error),
// otherwise the stored credentials (LoadToken).
func resolveToken(tokenFile string) (string, error) {
	if tokenFile != "" {
		return readTokenFile(tokenFile)
	}
	return LoadToken(), nil
}

// readTokenFile reads a token from path: a regular file, not a symbolic link
// (never followed: os.Lstat, then opened with O_NOFOLLOW and the opened file
// checked), not accessible to group or others (mode 0600 or stricter), owned
// by the current user, at most maxTokenFileBytes long; its content is the
// token with surrounding white space trimmed. The error never quotes the
// content.
func readTokenFile(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("--token-file: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("--token-file %s is a symbolic link; give the path of the file itself", path)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("--token-file %s is not a regular file", path)
	}
	f, err := openNoFollow(path)
	if err != nil {
		return "", fmt.Errorf("--token-file: %w", err)
	}
	defer f.Close()
	// Check the file that was opened (it may have been replaced since the
	// Lstat above).
	ofi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("--token-file: %w", err)
	}
	if !ofi.Mode().IsRegular() || !os.SameFile(fi, ofi) {
		return "", fmt.Errorf("--token-file %s changed while it was opened", path)
	}
	if perm := ofi.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("--token-file %s has mode %04o; it must be 0600 or stricter (chmod 600 %s)", path, perm, path)
	}
	if err := checkFileOwner(ofi); err != nil {
		return "", fmt.Errorf("--token-file %s: %w", path, err)
	}
	if ofi.Size() > maxTokenFileBytes {
		return "", fmt.Errorf("--token-file %s is larger than %d bytes", path, maxTokenFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read --token-file %s: %w", path, err)
	}
	if len(data) > maxTokenFileBytes {
		return "", fmt.Errorf("--token-file %s is larger than %d bytes", path, maxTokenFileBytes)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("--token-file %s is empty", path)
	}
	return token, nil
}

// LoadToken reads the stored BV-BRC token, returning empty string if not found.
func LoadToken() string {
	p, err := credentialsPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return ""
	}
	return creds.Token
}
