---
status: done
section: Add default shell spawn and @host shorthand to hoot
slug: add-default-shell-spawn-and-host-shorthand-to-hoot
mode: worktree
spec: spec.md
created: 2026-05-08T03:29:42Z
---

> ## Add default shell spawn and @host shorthand to hoot
>
> ---
> status:
>   type: open
> ---
>
> Make `hoot` (no args) and `hoot <host>` useful as quick shell-attach commands.
>
> - `hoot` — spawn and attach to the user's default shell (local).
> - `hoot @m4mini` — `@m4mini` is sugar for `--remote ssh://m4mini`; spawn and attach to a shell on that remote. The leading `@` always means "remote host".
> - `hoot m4mini` — same as `hoot @m4mini`, via subcommand fallthrough. If the first positional arg isn't a known subcommand, treat it as a remote host (mirrors `ssh m4mini`).
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Add `cmdShell` (local path) in `cmd/hoot/shell.go` with `--state-dir`, `--key`, `--no-reconnect`, `--prefix-key` flags
- [x] Resolve default shell helper (`$SHELL` → `/bin/sh`) + unit test
- [x] Add remote branch in `cmdShell`: empty argv → POST /sessions, attach via remote target
- [x] Server: `createSession` falls back to its own default shell when both `Argv` and `Cmd` are empty (instead of 400)
- [x] Wire top-level dispatch in `main.go`: bare `hoot`, `@host`, fallthrough host, flags-only, unknown first arg
- [x] Update `usage()` in `main.go` to document new forms
- [x] `cmd/hoot/shell_test.go` — local + remote spawn through fake hoot executable
- [x] `cmd/hoot/main_dispatch_test.go` — table test over routing decisions
- [x] Server-side default shell unit test
- [x] Update `README.md` with the new "quick shell" forms
- [x] `make test` clean
- [x] `make build` + manual smoke: `bin/hoot`, `bin/hoot run -- /bin/echo hi` regression, `bin/hoot help`

## Agent log
- 2026-05-08T03:34Z spec.md drafted; seeded 12-step todos. starting on cmdShell local path.
- 2026-05-08T03:42Z implementation + tests landed (sha 7e6b6e0); README updated (sha d8b9db1). full cmd/hoot test suite passing (6.3s); manual smoke shows bare hoot spawns /bin/zsh and attaches, @bogus fails at ssh resolution as expected.
- 2026-05-08T03:44Z status: done. evidence transcripts and full passing test suite logged in worklog. ready for boss review.

## Boss log

## Evidence

### Test suite

`make test` from the worktree, all green:

```
go test ./...
ok  	github.com/hayeah/hootty	7.733s
ok  	github.com/hayeah/hootty/cmd/hoot	7.090s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.489s
ok  	github.com/hayeah/hootty/internal/shortid	1.225s
ok  	github.com/hayeah/hootty/internal/sshtransport	1.590s
```

Targeted run for the new tests, verbose:

```
=== RUN   TestDispatchTopLevel
--- PASS: TestDispatchTopLevel (0.00s)
    --- PASS: .../no_args_spawns_local_shell
    --- PASS: .../@host_expands_to_ssh_remote
    --- PASS: .../@host_preserves_trailing_flags
    --- PASS: .../bare_host_falls_through_to_remote
    --- PASS: .../user@host_falls_through_(mirrors_ssh_user@host)
    --- PASS: .../known_subcommand_passes_through_unchanged
    --- PASS: .../ls_passes_through_to_main_switch_(which_aliases_to_list)
    --- PASS: .../help_routes_to_help,_no_shell_wrapping
    --- PASS: .../-h_routes_to_help
    --- PASS: .../--help_routes_to_help
    --- PASS: .../leading_flag_routes_through_shell
    --- PASS: .../leading_flag_with_no_value_still_goes_through_shell
=== RUN   TestDispatchTopLevelLsAlias
--- PASS: TestDispatchTopLevelLsAlias (0.00s)
=== RUN   TestDispatchTopLevel_AtSignAlone
--- PASS: TestDispatchTopLevel_AtSignAlone (0.00s)
=== RUN   TestKnownSubcommandsCoversDispatchSwitch
--- PASS: TestKnownSubcommandsCoversDispatchSwitch (0.00s)
=== RUN   TestCreateSessionDefaultShell
--- PASS: TestCreateSessionDefaultShell (0.11s)
=== RUN   TestCreateSessionExplicitCmdStillParses
--- PASS: TestCreateSessionExplicitCmdStillParses (0.11s)
=== RUN   TestResolveDefaultShell
--- PASS: TestResolveDefaultShell (0.00s)
=== RUN   TestCmdShellLocalSpawnsResolvedShell
--- PASS: TestCmdShellLocalSpawnsResolvedShell (0.08s)
=== RUN   TestCmdShellRemoteSendsEmptyArgv
--- PASS: TestCmdShellRemoteSendsEmptyArgv (0.00s)
=== RUN   TestCmdShellRejectsPositional
--- PASS: TestCmdShellRejectsPositional (0.00s)
PASS
```

### Manual smoke transcripts

`hoot help` shows the new forms first:

```
$ bin/hoot help
hoot — reference CLI for the session library

Quick shell:
  hoot                            # spawn local default shell, attach
  hoot @<host>                    # spawn shell on remote (ssh://<host>), attach
  hoot <host>                     # same as @<host> (subcommand fallthrough)

Usage:
  hoot run     [--remote <url>] [--state-dir <d>] [--key <k>] [--attach]
  ...
```

Bare `hoot` (with `--state-dir <tmp>` and `--key sh1` to make the
session inspectable) — stdin closed, so the attach prints connect /
disconnect banners and bails. The session entry confirms the local
$SHELL was resolved and spawned:

```
$ bin/hoot --state-dir "$TMP" --key sh1 < /dev/null
hoot: session "sh1" started (state-dir=/tmp/...)
[connected. sh1 @ local]
... (zsh prompt rendered, then SIGTERM from harness)
[disconnected. sh1 @ local]

$ bin/hoot list --state-dir "$TMP"
{"session":{"key":"sh1","argv":["/bin/zsh"],"cwd":"...","size":{"cols":80,"rows":24}},"state":{"state":"running","cmd":"/bin/zsh",...}}
```

`@host` translates to ssh and fails on a bogus DNS name (proves the
sugar reaches the ssh transport):

```
$ bin/hoot @bogus.invalid < /dev/null
hoot shell: Post "http://hoot/sessions": resolve remote home: exit status 255: ssh: Could not resolve hostname bogus.invalid: nodename nor servname provided, or not known
```

Subcommand fallthrough (`hoot bogus.invalid`) takes the same path:

```
$ bin/hoot bogus.invalid < /dev/null
hoot shell: Post "http://hoot/sessions": resolve remote home: exit status 255: ssh: Could not resolve hostname bogus.invalid: nodename nor servname provided, or not known
```

`hoot run` regression — still works as before:

```
$ bin/hoot run --state-dir "$TMP" -- /bin/echo hi
$ bin/hoot list --state-dir "$TMP"
{"session":{"key":"tsc","argv":["/bin/echo","hi"],...},"state":{"state":"exited","cmd":"/bin/echo hi",...}}
```

(Note: the `session did not start: timeout waiting for rpc.sock`
error is a pre-existing race when the spawned program exits faster
than the parent can stat rpc.sock; the session entry on disk shows
the spawn was successful. Same behavior on master before this branch.)

`hoot list` regression — existing sessions still surface:

```
$ bin/hoot list  # existing global state-dir
{"session":{"key":"reuse112157","argv":["bash","-lc","..."]},...}
{"session":{"key":"098","argv":["zsh","-l"]},...}
```

### Commits

- `7e6b6e0` Add bare hoot, @host, and host-fallthrough shell-spawn
- `d8b9db1` README: document bare hoot, @host, and fallthrough host forms

## Trouble report

- The `dispatchTopLevel` table test initially failed for `help`/`-h`/`--help`
  because Go's `reflect.DeepEqual([]string{}, nil)` is false. Fixed by
  normalizing empty rest slices to nil at the dispatch boundary so
  callers (and tests) see one canonical "no args" representation.
- The remote shell test originally asserted on every server request,
  which broke when `runAttachLoop` made a follow-up HUD-load call.
  Switched the test handler to only validate the POST /sessions and
  let downstream calls fall through with 404 to fail-fast the attach
  loop. No production change needed.
- The pre-existing `hoot run -- /bin/echo hi` "session did not start"
  race surfaced again during regression smoke; not introduced by this
  branch (echo exits before the parent stats rpc.sock). Left as-is —
  outside the section's scope.
- Unrelated pre-existing diagnostic surfaced from go vet about `errors.As`
  potentially being simplified to `errors.AsType[*exitError]`. The
  pattern matches what `cmdRun` already does in `run.go`; not
  touching the existing convention from a section about a different
  feature.
