package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/sessionpick"
)

// cmdList renders the local (or `--remote`) session list. By default
// it prints one human-readable line per live session — the same line
// shape the fzf picker matches against (see sessionpick.Format).
//
//   - default: pretty lines, alive only
//   - --all: include exited sessions (rendered with a `(dead)` tag)
//   - --json: emit JSONL of session.StateFile (the original
//     scripting-friendly shape). Honors --all the same way.
func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (host, user@host, host:port — defaults to ssh://; or http(s)://host:port, ssh://host)")
	jsonOut := fs.Bool("json", false, "emit JSONL of session.StateFile instead of human-readable lines")
	all := fs.Bool("all", false, "include exited (dead) sessions; default is live only")
	if err := fs.Parse(args); err != nil {
		return err
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return err
	}
	if remote != nil {
		defer remote.Close()
	}

	states, err := loadSessionList(remote, *stateDir)
	if err != nil {
		return err
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, sm := range states {
			if !*all && !sm.Alive {
				continue
			}
			if sm.State == nil {
				continue
			}
			if err := enc.Encode(sm.State); err != nil {
				return err
			}
		}
		return nil
	}

	for _, sm := range states {
		if !*all && !sm.Alive {
			continue
		}
		fmt.Println(sessionpick.Format(sm))
	}
	return nil
}

// cmdResolve takes a full id or a unique prefix and prints the
// resolved session key. Exits non-zero with the shortid error
// (ambiguous / too short / not found) on failure.
func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (host, user@host, host:port — defaults to ssh://; or http(s)://host:port, ssh://host)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("resolve: expected exactly one <id-or-prefix> argument")
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return err
	}
	if remote != nil {
		defer remote.Close()
		resp, err := httpClient(remote).Get("http://hoot/sessions/" + url.PathEscape(rest[0]) + "/resolve")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return remoteResponseError(resp)
		}
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return err
		}
		fmt.Println(body.Key)
		return nil
	}

	store := session.NewStore(*stateDir)
	state, err := store.Resolve(rest[0])
	if err != nil {
		return err
	}
	fmt.Println(state.Session.Key)
	return nil
}
