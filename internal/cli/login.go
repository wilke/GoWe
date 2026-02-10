package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
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
