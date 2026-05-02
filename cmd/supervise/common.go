package main

import (
	"os"
	"path/filepath"
)

// defaultStateDir resolves to ~/.supervise.
func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".supervise"
	}
	return filepath.Join(home, ".supervise")
}
