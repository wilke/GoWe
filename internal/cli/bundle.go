package cli

import (
	"fmt"
	"os"

	"github.com/me/gowe/internal/bundle"
	"github.com/spf13/cobra"
)

func newBundleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle <workflow.cwl>",
		Short: "Bundle a CWL workflow into a packed $graph document",
		Long:  "Reads a CWL workflow file, resolves all run: references, and outputs a packed $graph YAML document to stdout.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := bundle.Bundle(args[0])
			if err != nil {
				return fmt.Errorf("bundle: %w", err)
			}
			_, err = os.Stdout.Write(result.Packed)
			return err
		},
	}
	return cmd
}
