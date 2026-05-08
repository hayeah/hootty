---
status: done
section: Add --cwd and env flags to hoot run
slug: add-cwd-and-env-flags-to-hoot-run
mode: worktree
spec:
created: 2026-05-08T03:03:42Z
---

> ## Add --cwd and env flags to hoot run
>
> ---
> status:
>   type: open
> ---
>
> Extend `hoot run` with launch-config flags:
>
> - `--cwd <path>` — set the working directory when launching
> - `--env FOO=bar` — set an env var; repeatable so multiple `--env` flags compose
> - `--env-file <path>` — load env vars from a dotenv file
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] add `--cwd`, `--env`, `--env-file` flags to `cmdRun` (cmd/hoot/run.go); reuse `parseEnvOverrides` + `stringListFlag` from clone
- [x] thread cwd + env into local spawn (`spawnSessionFromSpec`) and remote create (`createSessionReq` body, `serveState.createSession`)
- [x] resolve `--cwd` to absolute; empty falls back to `os.Getwd`
- [x] update `usage()` in cmd/hoot/main.go and `## hoot CLI` block in README.md
- [x] add tests: local run (fake hoot script captures `--cwd` + env vars), relative-cwd absolutization, remote run (asserts createSessionReq carries CWD + Env), and `resolveRunCWD` unit
- [x] `go vet ./...` + `go test ./...`
- [x] commit + populate `## Evidence`

## Agent log
- 2026-05-08T03:12Z implemented + tested --cwd / --env / --env-file on hoot run; commit 9e28012
- 2026-05-08T03:13Z status=done; evidence + smoke transcripts captured; ready for lgtm

## Boss log

## Evidence

Repo: `github.com/hayeah/hootty`, branch `add-cwd-and-env-flags-to-hoot-run`, commit `9e28012`.

### Unit tests (cmd/hoot)

```
$ go test ./...
ok  	github.com/hayeah/hootty	7.711s
ok  	github.com/hayeah/hootty/cmd/hoot	(cached)
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.401s
ok  	github.com/hayeah/hootty/internal/shortid	1.822s
ok  	github.com/hayeah/hootty/internal/sshtransport	1.481s
```

New tests added in `cmd/hoot/run_test.go`:

- `TestCmdRunHonorsCWDAndEnv` — drives the local spawn through the
  fake `hoot` shim and asserts `state.json` records `--cwd` and that
  `--env CLONE_VAR=run-direct` + `--env-file` (carrying `RUN_FILE_VAR`)
  reach the child's environment.
- `TestCmdRunRelativeCWDIsAbsolutized` — chdir to a temp parent, pass
  `--cwd sub`, assert the recorded `session.cwd` resolves to the
  absolute child dir (not the relative literal).
- `TestCmdRunRemoteForwardsCWDAndEnv` — `httptest` server captures the
  `POST /sessions` body and asserts `Argv`, `CWD`, and `Env`
  (with `--env DIRECT=yes` + `--env-file FROM_FILE=ok`) flow through.
- `TestResolveRunCWD` — empty falls back to `os.Getwd`, absolute is
  preserved, relative is absolutized.

`clone_test.go` fake-hoot script now also records `RUN_FILE_VAR` so the
new run tests can reuse it; existing clone test (`clone_var`) unchanged.

### Smoke run against the built binary

`bin/hoot run --cwd /tmp/hoot-smoke/work --env HOOT_DIRECT=hello --env-file /tmp/hoot-smoke.env --key smoke1 -- bash -lc '<dump cwd + matching env to file>'`:

```
hoot: session "smoke1" started (state-dir=/tmp/hoot-smoke/state)
smoke1
--- pwd ---
/private/tmp/hoot-smoke/work          # macOS resolves /tmp/...
--- env ---
HOOT_DIRECT=hello
HOOT_FROM_FILE=fromfile
--- state.json session.cwd ---
/tmp/hoot-smoke/work
```

`HOOT_LOOKUP=looked-up bin/hoot run --env HOOT_LOOKUP ...` (env lookup
form, NAME without `=VALUE`):

```
HOOT_LOOKUP=looked-up
```

Error path — malformed env file:

```
$ bin/hoot run --env-file /tmp/hoot-bad.env -- bash -c true
hoot run: --env-file /tmp/hoot-bad.env:1: expected NAME=VALUE
exit=1
```

### `--help` output (build)

```
$ ./bin/hoot run --help
Usage of run:
  -attach        attach to the new session after it starts
  -cwd string    working directory for the spawned command (default: current directory)
  -env value     env override NAME or NAME=VALUE (repeatable)
  -env-file value  dotenv-style env override file (repeatable)
  -key string    session key (default: random short id)
  -no-reconnect  exit on first drop instead of auto-reconnecting during post-spawn attach (--remote only)
  -prefix-key string  command prefix byte for post-spawn attach (default "C-^")
  -remote string remote hoot serve URL (...)
  -state-dir string session state directory (default "/Users/me/.hoot")
```

## Trouble report

- Reused `parseEnvOverrides` + `stringListFlag` from `cmd/hoot/clone.go`
  unchanged — the env-flag semantics for `run` and `clone` should match
  so a session can be re-run with the same overrides as a clone-of-X.
- The remote create-session body now carries `cwd` + `env`, but `cwd` is
  passed through *raw* (not absolutized locally). The server side
  absolutizes via `resolveServeCWD`. Rationale: a relative `--cwd` over
  `--remote` would otherwise be evaluated against the local cwd, which
  is meaningless on the remote box. Empty stays empty → server falls
  back to its own `os.Getwd`. Worth flagging if the boss wants different
  semantics here.
- Gopls reported a stream of "undefined" diagnostics on the worktree
  copy throughout the session — same-package symbols (`cmdRun`,
  `serveState`, `spawnSpec`, …) flagged as undeclared. Workspace-config
  noise (the worktree isn't in any `go.work`); `make vet`, `go test
  ./...`, and the built binary all see them fine, so I ignored the LSP
  warnings and trusted the compiler. #friction worth noting if the boss
  wants the worktrees added to a workspace file.
