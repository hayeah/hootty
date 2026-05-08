package main

import (
	"fmt"
	"os"
)

// hootSessionEnv is the env var the session library publishes to
// every spawned child. Non-empty means "you're already inside a hoot
// attach"; nested attach paths key off it to refuse a second
// terminal takeover, mirroring tmux's $TMUX convention.
const hootSessionEnv = "HOOT_SESSION"

// errIfNestedHootSession returns a tmux-shaped error when $HOOT_SESSION
// is set in the current process env, naming the env var to unset for
// an override. Verb is the user-facing form ("attach", "shell",
// "run --attach"); it goes into the message so the user knows which
// command refused. Returns nil when the env is empty (the common
// non-nested case).
func errIfNestedHootSession(verb string) error {
	key := os.Getenv(hootSessionEnv)
	if key == "" {
		return nil
	}
	return fmt.Errorf(
		"sessions should be nested with care, unset $%s to force (already attached to %q)",
		hootSessionEnv, key,
	)
}
