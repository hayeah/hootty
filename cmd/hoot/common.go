package main

import (
	"os"
	"path/filepath"
)

// defaultStateDir resolves to ~/.hoot.
func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".hoot"
	}
	return filepath.Join(home, ".hoot")
}
