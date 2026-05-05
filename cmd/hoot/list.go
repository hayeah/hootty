package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/hayeah/hootty"
)

// cmdList emits one JSON object per line — each line is a
// hootty.StateFile serialized as-is. No bespoke list view, no
// derived fields: callers that need liveness can probe the flock
// themselves, or call `hoot resolve` and look at the hootty
// PID.
func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store := hootty.NewStore(*stateDir)
	states, err := store.List()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	for _, st := range states {
		if err := enc.Encode(st); err != nil {
			return err
		}
	}
	return nil
}

// cmdResolve takes a full id or a unique prefix and prints the
// resolved session key. Exits non-zero with the shortid error
// (ambiguous / too short / not found) on failure.
func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("resolve: expected exactly one <id-or-prefix> argument")
	}

	store := hootty.NewStore(*stateDir)
	state, err := store.Resolve(rest[0])
	if err != nil {
		return err
	}
	fmt.Println(state.Hootty.Key)
	return nil
}
