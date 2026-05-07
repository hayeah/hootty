package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/hayeah/hootty"
)

var (
	errCloneCorruptState = errors.New("clone source has corrupt argv/cwd metadata")
	errCloneKeyAlive     = errors.New("clone destination key is alive")
)

type cloneSessionReq struct {
	Key string            `json:"key,omitempty"`
	Env map[string]string `json:"env,omitempty"`
}

type cloneSessionResp struct {
	*session.StateFile
	Alive      bool   `json:"alive"`
	SocketPath string `json:"socket_path"`
}

func cmdClone(args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: hoot clone [flags] [<id-or-prefix-or-pattern>]

Spawn a fresh sibling session from a source session's recorded argv +
cwd. With no positional argument (and on a tty), an interactive fzf
picker selects the source. With one argument, an id-prefix match wins;
on miss the picker re-runs preseeded with --query=<arg> --select-1
--exit-0 so a unique fuzzy hit auto-clones. --strict disables both
fallbacks (id-prefix only).

See `+"`hoot attach -h`"+` for the picker line format and fzf query
vocabulary.

Flags:
  --remote <url>        remote hoot serve URL
  --state-dir <d>       session state directory (default ~/.hoot)
  --key <k>             new session key (default: random short id)
  --env NAME[=VALUE]    one-shot env override (repeatable)
  --env-file <path>     dotenv-style override file (repeatable)
  --attach              attach to the cloned session after creating it
  --detach              do not attach after clone (default)
  --strict              exact id-prefix match only — no fzf, no picker
`)
	}
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	key := fs.String("key", "", "new session key (default: random short id)")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (host, user@host, host:port — defaults to ssh://; or http(s)://host:port, ssh://host)")
	attach := fs.Bool("attach", false, "attach to the cloned session after creating it")
	detach := fs.Bool("detach", false, "do not attach after clone (default)")
	strict := fs.Bool("strict", false, "exact id-prefix match only — no fzf, no picker")
	var envSpecs stringListFlag
	var envFiles stringListFlag
	fs.Var(&envSpecs, "env", "clone env override NAME or NAME=VALUE (repeatable)")
	fs.Var(&envFiles, "env-file", "dotenv-style clone env override file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		fs.Usage()
		return errors.New("clone: expected at most one <id-or-prefix> argument")
	}
	if *attach && *detach {
		return errors.New("clone: --attach and --detach conflict")
	}
	env, err := parseEnvOverrides(envFiles, envSpecs, os.LookupEnv)
	if err != nil {
		return err
	}
	arg := ""
	if len(rest) == 1 {
		arg = rest[0]
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return err
	}
	if remote != nil {
		defer remote.Close()
	}

	source, code, err := resolveSessionKey(arg, *strict, remote, *stateDir, pickerOptions{Verb: "clone"})
	if err != nil {
		return &exitError{code: code, err: err}
	}

	if remote != nil {
		body, err := cloneSessionRemote(remote, source, *key, env)
		if err != nil {
			return err
		}
		fmt.Println(body.Session.Key)
		if *attach {
			code := cmdAttach([]string{"--remote", *remoteFlag, body.Session.Key})
			if code != 0 {
				return fmt.Errorf("attach exited with code %d", code)
			}
		}
		return nil
	}

	store := session.NewStore(*stateDir)
	body, err := cloneSession(store, *stateDir, source, *key, env)
	if err != nil {
		return err
	}
	fmt.Println(body.Session.Key)
	if *attach {
		code := cmdAttach([]string{"--state-dir", *stateDir, body.Session.Key})
		if code != 0 {
			return fmt.Errorf("attach exited with code %d", code)
		}
	}
	return nil
}

func cloneSession(store *session.Store, stateDir, sourceQuery, newKey string, env map[string]string) (*cloneSessionResp, error) {
	source, err := store.Resolve(sourceQuery)
	if err != nil {
		return nil, err
	}
	if len(source.Session.Argv) == 0 || source.Session.CWD == "" {
		return nil, fmt.Errorf("%w: %q", errCloneCorruptState, source.Session.Key)
	}
	key := newKey
	if key == "" {
		generated, err := generateKey(store)
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
		key = generated
	}
	if store.IsAlive(key) {
		return nil, fmt.Errorf("%w: %q", errCloneKeyAlive, key)
	}
	sockPath, err := spawnSessionFromSpec(stateDir, key, spawnSpec{
		Argv:         source.Session.Argv,
		CWD:          source.Session.CWD,
		EnvOverrides: env,
		Cols:         80,
		Rows:         24,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn: %w", err)
	}
	st, err := store.Load(key)
	if err != nil {
		return nil, fmt.Errorf("load state after spawn: %w", err)
	}
	return &cloneSessionResp{
		StateFile:  st,
		Alive:      true,
		SocketPath: sockPath,
	}, nil
}

func cloneSessionRemote(remote *Remote, sourceQuery, newKey string, env map[string]string) (*cloneSessionResp, error) {
	payload, err := json.Marshal(cloneSessionReq{Key: newKey, Env: env})
	if err != nil {
		return nil, err
	}
	path := "http://hoot/sessions/" + url.PathEscape(sourceQuery) + "/clone"
	resp, err := httpClient(remote).Post(path, "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, remoteResponseError(resp)
	}
	var body cloneSessionResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.StateFile == nil || body.Session.Key == "" {
		return nil, errors.New("remote clone: response missing session key")
	}
	return &body, nil
}
