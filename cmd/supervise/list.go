package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hayeah/supervisor"
)

// cmdList prints all sessions found under --state-dir, one per line:
//
//	<key>\t<alive|dead>\t<created_at>
func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store := supervisor.NewStore(*stateDir)
	states, err := store.List()
	if err != nil {
		return err
	}
	for _, st := range states {
		alive := "dead"
		if store.IsAlive(st.Supervisor.Key) {
			alive = "alive"
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n",
			st.Supervisor.Key, alive,
			st.Supervisor.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		)
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

	store := supervisor.NewStore(*stateDir)
	state, err := store.Resolve(rest[0])
	if err != nil {
		return err
	}
	fmt.Println(state.Supervisor.Key)
	return nil
}
