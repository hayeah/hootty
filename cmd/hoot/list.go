package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/hayeah/hootty"
)

// cmdList emits one JSON object per line — each line is a
// session.StateFile serialized as-is. No bespoke list view, no
// derived fields: callers that need liveness can probe the flock
// themselves, or call `hoot resolve` and look at the session
// PID.
func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (http://host:port, host:port, or ssh://host)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return err
	}
	if remote != nil {
		defer remote.Close()
		resp, err := httpClient(remote).Get("http://hoot/sessions")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return remoteResponseError(resp)
		}
		var body struct {
			Sessions []struct {
				session.StateFile
				Alive bool `json:"alive"`
			} `json:"sessions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		for _, st := range body.Sessions {
			if err := enc.Encode(st.StateFile); err != nil {
				return err
			}
		}
		return nil
	}

	store := session.NewStore(*stateDir)
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
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (http://host:port, host:port, or ssh://host)")
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
