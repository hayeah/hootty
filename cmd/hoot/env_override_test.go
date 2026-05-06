package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "clone.env")
	if err := os.WriteFile(envFile, []byte(`
# comment
FROM_FILE=old
FILE_ONLY=ok
EMPTY=
`), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	got, err := parseEnvOverrides(
		[]string{envFile},
		[]string{"FROM_FILE=new", "FROM_LOOKUP", "LITERAL="},
		func(name string) (string, bool) {
			if name == "FROM_LOOKUP" {
				return "lookup-value", true
			}
			return "", false
		},
	)
	if err != nil {
		t.Fatalf("parseEnvOverrides: %v", err)
	}

	want := map[string]string{
		"FROM_FILE":   "new",
		"FILE_ONLY":   "ok",
		"EMPTY":       "",
		"FROM_LOOKUP": "lookup-value",
		"LITERAL":     "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got %s=%q, want %q (all=%v)", k, got[k], v, got)
		}
	}
}

func TestParseEnvOverridesErrors(t *testing.T) {
	t.Run("missing lookup", func(t *testing.T) {
		_, err := parseEnvOverrides(nil, []string{"MISSING"}, func(string) (string, bool) {
			return "", false
		})
		if err == nil || !strings.Contains(err.Error(), "not set") {
			t.Fatalf("err = %v, want not set", err)
		}
	})

	t.Run("bad env name", func(t *testing.T) {
		_, err := parseEnvOverrides(nil, []string{"1BAD=value"}, func(string) (string, bool) {
			return "", false
		})
		if err == nil || !strings.Contains(err.Error(), "invalid environment variable name") {
			t.Fatalf("err = %v, want invalid name", err)
		}
	})

	t.Run("malformed env file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.env")
		if err := os.WriteFile(path, []byte("NO_EQUALS\n"), 0o644); err != nil {
			t.Fatalf("write env file: %v", err)
		}
		_, err := parseEnvOverrides([]string{path}, nil, func(string) (string, bool) {
			return "", false
		})
		if err == nil || !strings.Contains(err.Error(), "expected NAME=VALUE") {
			t.Fatalf("err = %v, want malformed line", err)
		}
	})
}
