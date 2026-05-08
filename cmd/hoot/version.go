package main

import (
	"fmt"
	"io"
	"os"
)

// Version is the released version string. Release builds inject the
// tag via `go build -ldflags "-X main.Version=vX.Y.Z"`. The "dev"
// default is what unreleased builds (and unit tests) report.
var Version = "dev"

func cmdVersion(args []string) error {
	return runVersion(args, os.Stdout)
}

func runVersion(args []string, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("usage: hoot version (no arguments)")
	}
	fmt.Fprintln(out, Version)
	return nil
}
