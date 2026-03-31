package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/me/gowe/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		var unsupported *cli.UnsupportedRequirementError
		if errors.As(err, &unsupported) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(33)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
