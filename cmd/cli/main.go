package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/me/gowe/internal/cli"
)

// Version is the build version, stamped at release time via -ldflags "-X main.Version=...".
var Version = "dev"

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
