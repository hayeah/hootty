package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion_PrintsOneLineNoDecoration(t *testing.T) {
	prev := Version
	t.Cleanup(func() { Version = prev })
	Version = "v1.2.3"

	var buf bytes.Buffer
	if err := runVersion(nil, &buf); err != nil {
		t.Fatalf("runVersion: %v", err)
	}
	got := buf.String()
	want := "v1.2.3\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("expected exactly one trailing newline, got %q", got)
	}
}

func TestRunVersion_RejectsExtraArgs(t *testing.T) {
	var buf bytes.Buffer
	err := runVersion([]string{"--foo"}, &buf)
	if err == nil {
		t.Fatal("expected error on extra args, got nil")
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output on error, got %q", buf.String())
	}
}
