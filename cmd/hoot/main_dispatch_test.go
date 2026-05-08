package main

import (
	"reflect"
	"testing"
)

// TestDispatchTopLevel locks down the new top-level dispatch matrix:
// what bare `hoot`, `@host`, fallthrough host, flags-only, known
// subcommands, and `--help` all translate into.
func TestDispatchTopLevel(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantCmd  string
		wantArgs []string
	}{
		{
			name:    "no args spawns local shell",
			argv:    nil,
			wantCmd: "shell",
		},
		{
			name:     "@host expands to ssh remote",
			argv:     []string{"@m4mini"},
			wantCmd:  "shell",
			wantArgs: []string{"--remote", "ssh://m4mini"},
		},
		{
			name:     "@host preserves trailing flags",
			argv:     []string{"@m4mini", "--no-reconnect", "--prefix-key", "C-a"},
			wantCmd:  "shell",
			wantArgs: []string{"--remote", "ssh://m4mini", "--no-reconnect", "--prefix-key", "C-a"},
		},
		{
			name:     "bare host falls through to remote",
			argv:     []string{"m4mini"},
			wantCmd:  "shell",
			wantArgs: []string{"--remote", "ssh://m4mini"},
		},
		{
			name:     "user@host falls through (mirrors ssh user@host)",
			argv:     []string{"me@devbox"},
			wantCmd:  "shell",
			wantArgs: []string{"--remote", "ssh://me@devbox"},
		},
		{
			name:     "known subcommand passes through unchanged",
			argv:     []string{"run", "--", "bash", "-l"},
			wantCmd:  "run",
			wantArgs: []string{"--", "bash", "-l"},
		},
		{
			name:     "ls passes through to main switch (which aliases to list)",
			argv:     []string{"ls"},
			wantCmd:  "ls",
			wantArgs: nil,
		},
		{
			name:     "help routes to help, no shell wrapping",
			argv:     []string{"help"},
			wantCmd:  "help",
			wantArgs: nil,
		},
		{
			name:     "-h routes to help",
			argv:     []string{"-h"},
			wantCmd:  "help",
			wantArgs: nil,
		},
		{
			name:     "--help routes to help",
			argv:     []string{"--help"},
			wantCmd:  "help",
			wantArgs: nil,
		},
		{
			name:     "leading flag routes through shell",
			argv:     []string{"--remote", "ssh://m4mini"},
			wantCmd:  "shell",
			wantArgs: []string{"--remote", "ssh://m4mini"},
		},
		{
			name:     "leading flag with no value still goes through shell",
			argv:     []string{"--no-reconnect"},
			wantCmd:  "shell",
			wantArgs: []string{"--no-reconnect"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCmd, gotArgs, ok := dispatchTopLevel(tc.argv)
			if !ok {
				t.Fatalf("ok=false for argv=%v", tc.argv)
			}
			if gotCmd != tc.wantCmd {
				t.Fatalf("cmd = %q, want %q", gotCmd, tc.wantCmd)
			}
			// ls aliases to "list" but the cmd switch retains the
			// original arg list — ls case has wantCmd="list" but the
			// dispatcher only normalizes the verb in its return for
			// known list/ls; here our dispatcher doesn't actually
			// rewrite ls→list (the switch in main does). Adjust:
			// we asserted via wantCmd above only for clarity.
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Fatalf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

// TestDispatchTopLevelLsAlias documents ls passing through verbatim;
// the verb switch in main() handles the alias.
func TestDispatchTopLevelLsAlias(t *testing.T) {
	cmd, args, ok := dispatchTopLevel([]string{"ls", "--state-dir", "/tmp/foo"})
	if !ok {
		t.Fatalf("ok=false")
	}
	if cmd != "ls" {
		t.Fatalf("cmd = %q, want ls (alias resolution happens in main switch)", cmd)
	}
	if !reflect.DeepEqual(args, []string{"--state-dir", "/tmp/foo"}) {
		t.Fatalf("args = %v, want flag passthrough", args)
	}
}

// TestDispatchTopLevel_AtSignAlone — a lone `@` is not a host
// shorthand; we treat it as fallthrough (passes "@" as the host
// to ssh, which will fail, but that's the user's problem).
//
// We assert it doesn't crash and we don't accidentally route it as
// a known subcommand.
func TestDispatchTopLevel_AtSignAlone(t *testing.T) {
	cmd, args, ok := dispatchTopLevel([]string{"@"})
	if !ok {
		t.Fatalf("ok=false")
	}
	// "@" is not >1 char so the @host strip branch doesn't fire;
	// it falls into the unknown-positional fallthrough.
	if cmd != "shell" {
		t.Fatalf("cmd = %q, want shell", cmd)
	}
	if !reflect.DeepEqual(args, []string{"--remote", "ssh://@"}) {
		t.Fatalf("args = %v, want fallthrough wrapping", args)
	}
}

// TestKnownSubcommandsCoversDispatchSwitch is a guardrail: every key
// in knownSubcommands must be a verb the main switch can handle (or
// "help" / "-h" / "--help"). If someone adds a subcommand to one and
// forgets the other, this test catches it.
func TestKnownSubcommandsCoversDispatchSwitch(t *testing.T) {
	expected := map[string]bool{
		"run": true, "list": true, "ls": true, "resolve": true,
		"attach": true, "clone": true, "kill": true, "detach": true,
		"write": true, "serve": true, "__session": true,
		"-h": true, "--help": true, "help": true,
	}
	if !reflect.DeepEqual(knownSubcommands, expected) {
		t.Fatalf("knownSubcommands drifted from expected: got %v, want %v", knownSubcommands, expected)
	}
}
