# Add default shell spawn and `@host` shorthand to hoot

## Goal

Make `hoot` (no args) and `hoot <host>` useful as a quick "open me a shell" command — the way `ssh` is for remote shells today, but with hoot's session/attach machinery underneath.

Three new top-level forms in `cmd/hoot/main.go`:

- `hoot` → spawn the user's default shell locally and attach. Equivalent to today's `hoot run --attach -- $SHELL` plus a sane $SHELL fallback.
- `hoot @m4mini` → sugar for `hoot --remote ssh://m4mini` with the implicit shell-spawn semantics. The leading `@` always means "remote host"; this is the unambiguous form.
- `hoot m4mini` → same as `hoot @m4mini`, via a subcommand fallthrough: if the first positional arg isn't a known subcommand (and isn't a flag), treat it as a remote ssh host. Mirrors `ssh m4mini`.

Existing subcommands (`run`, `list`, `attach`, …) keep their current behavior. Passing flags for the no-subcommand form (e.g. `hoot --state-dir /tmp/foo`) is also supported via the same shell-spawn path, so the new behavior composes cleanly with existing flags users already know from `run` and `attach`.

Out of scope: changes to `run`, the picker, server routing other than the empty-argv default, or any other surface besides the new top-level dispatch.

## Architecture

### Files touched

- `cmd/hoot/main.go` — new top-level dispatch: `@host`, no-subcommand, and unknown-first-arg fallthrough.
- `cmd/hoot/shell.go` (new) — `cmdShell(args)` that parses a small flag set, resolves a shell argv, and goes through the existing spawn-and-attach machinery (local or remote).
- `cmd/hoot/serve_spawn.go` — when `createSessionReq.Argv` is empty AND `Cmd` is empty, resolve a default shell on the server side instead of returning 400. This lets `hoot @host` send empty argv and let the remote pick its own $SHELL.
- `cmd/hoot/main.go` — extend `usage()` to document the new forms.
- `README.md` — add a short "Quick shell" section near the top of the CLI surface description.
- Tests: `cmd/hoot/shell_test.go`, `cmd/hoot/main_dispatch_test.go`, plus targeted additions to `serve_spawn_test.go` (or wherever the spawn handler is exercised) to cover empty-argv → default shell.

### Public CLI shape

```
hoot                      # local default shell, attached
hoot @m4mini              # ssh://m4mini default shell, attached
hoot m4mini               # same as @m4mini (fallthrough)
hoot user@m4mini          # ssh user@m4mini default shell, attached
hoot --remote ssh://x     # explicit form; same dispatch as bare hoot
hoot @m4mini --no-reconnect --prefix-key C-a   # flags after host work too
```

Old behavior preserved:

```
hoot run -- bash          # still works
hoot list                 # still works
hoot --help, -h, help     # still works
hoot foo                  # NEW: was "unknown subcommand", now treated as @foo
                          #      — covered explicitly in the section text
```

### Default shell resolution

Local (`cmdShell` in the no-`--remote` branch):

1. `os.Getenv("SHELL")` if non-empty.
2. Else `/bin/sh` (POSIX guarantee).

We do NOT consult `os/user` + parse `/etc/passwd`: $SHELL is what `man 5 environ` and every shell that spawns a child sets, and falling back to `/bin/sh` is what `system(3)` does. Keep it simple.

Remote (server-side, in `createSession`):

1. Server's `os.Getenv("SHELL")` if non-empty.
2. Else `/bin/sh`.

For `hoot @m4mini` the local CLI sends an empty `argv` over the JSON body, the remote `hoot serve` resolves on the remote side. This means the right $SHELL for that user on that host gets picked, even if the local shell is different.

### Argv: bare shell, not `-l`

We invoke the shell as `[shell]` — a single arg, no `-l` (login) and no `-i` (interactive). Reasoning:

- A PTY-attached shell already auto-detects interactive (`isatty`) — bash and zsh both promote to interactive without `-i` when stdin/stdout are a tty.
- `-l` (login) re-runs `~/.zprofile`/`~/.bash_profile`, which on macOS includes Path Helper / `nvm` setup that's already inherited from the parent process. Re-running it is a noticeable startup delay and occasionally introduces duplicate $PATH entries. Users who want a login shell can `hoot run -- bash -l` explicitly.

This matches `ssh <host>` (no flags → interactive non-login on most setups) and `tmux new-window` defaults.

### Subcommand fallthrough rules

In `main.go`, after stripping `os.Args[0]`:

```
len(args) == 0                                  → cmdShell(nil)
args[0] == "@<host>" (len > 1)                  → cmdShell(["--remote", "ssh://<host>"] + args[1:])
args[0] in known subcommand set                 → existing dispatch
args[0] starts with "-"                         → cmdShell(args)            // flags only
args[0] is anything else                        → cmdShell(["--remote", "ssh://<args[0]>"] + args[1:])
```

Known subcommand set: `run`, `list`, `ls`, `resolve`, `attach`, `clone`, `kill`, `detach`, `write`, `serve`, `__session`, `-h`, `--help`, `help`. (`-h` and `--help` already in the dispatch as help; we keep those listed explicitly so they're not treated as flags into `cmdShell`.)

### `cmdShell` flag set

A subset of `cmdRun`'s flags — only the ones meaningful for "spawn + attach a shell":

- `--remote <url>` — same parsing as `cmdRun` (`parseRemoteFlag`).
- `--state-dir <d>` — same default.
- `--key <k>` — same.
- `--no-reconnect`, `--prefix-key <key>` — same as the post-spawn attach in `cmdRun`.

Skipped vs. `cmdRun`:

- `--attach` — always implicit. The whole point of `hoot` and `hoot @host` is to attach.
- `--cwd`, `--env`, `--env-file` — keep the surface tight; users who need overrides should use `hoot run`. Easy to add later if asked.
- positional `-- <cmd>` — there is no `--`. The argv is always the resolved default shell.

### Display / banner

The post-spawn banner from `cmdRun` already prints `hoot: session %q started (state-dir=%s)` (local) or `(remote=%s)` (remote). We reuse those without changes.

## Steps

1. Read the existing dispatch in `main.go` and the cmdRun flag plumbing top-to-bottom; sketch the `cmdShell` skeleton on paper.
2. Add `cmdShell` in `cmd/hoot/shell.go`. Local-only path first (no `--remote`): resolve $SHELL, call `spawnSessionFromSpec` and `runAttachLoop` like `cmdRun` does. Smoke-test by hand with `bin/hoot`.
3. Add the remote branch in `cmdShell`: send `createSessionReq{Argv: []string{}, Key: ...}` to the remote and attach via the same code path as `cmdRun --remote --attach`.
4. Update `serve_spawn.go` to fall back to a server-side default shell when both `Argv` and `Cmd` are empty. Add a unit test in the right `_test.go` covering both the old "still 400 if you set Cmd to whitespace junk" case and the new empty-both → default shell.
5. Wire the new top-level dispatch in `main.go`: handle `@host`, no-args, and unknown-first-arg fallthrough. Keep `-h/--help/help` and the known subcommand list as the only forms that don't go through `cmdShell`.
6. Add tests:
   - `shell_test.go` — `cmdShell` flag parsing, local default-shell resolution helper (`resolveDefaultShell` or similar), local dispatch via `withFakeHootExecutable` (mirror `run_test.go`).
   - `main_dispatch_test.go` — direct table-test over the new top-level routing decisions: empty argv, `@host`, `host`, `host with flags`, `flags only`, `unknown flag`, plus the boundary cases (`-h`, `help`).
   - `serve_spawn_test.go` (or wherever the existing handler tests live) — empty-both falls back to the server's $SHELL.
7. Update `usage()` in `main.go` and `README.md` with the new forms.
8. Run the full test suite (`make test`) and fix anything that drifted.
9. Build `bin/hoot` and run a smoke transcript: `hoot` (local), `hoot @localhost` (ssh loopback if available, else skip), `hoot run -- bash` (regression).

## Verification

- `go test ./cmd/hoot/...` passes, with the new tests visibly covering: bare `hoot` → local shell, `@host` → ssh shorthand, fallthrough host → ssh shorthand, known subcommand still dispatches to its existing handler, `--help` still prints usage, and the server-side empty-argv default shell.
- A manual transcript pasted into `## Evidence`:
  - `bin/hoot` → opens a shell, prompt visible, `<prefix>.` detaches, `hoot list` shows the session.
  - `bin/hoot run -- /bin/echo hi` regression — still spawns and exits.
  - `bin/hoot help` and `bin/hoot --help` still print usage with the new forms documented.
- README diff shows the new "quick shell" example.

## Open questions

None. The shape is small and the section text is unambiguous about the three forms; remaining decisions (no `-l`, $SHELL fallback to `/bin/sh`, server-side resolve for remote) are documented under Architecture and revisitable if pushback arrives.

## Design notes

- 2026-05-08T03:35Z — Picked **server-side $SHELL resolution for remote** over **local $SHELL forwarding**.
  - Alternatives:
    - Local $SHELL forwarding: simpler (one less server change), but breaks when local and remote use different shells (fish locally, zsh remote → "fish: not found"). Common case for SSHing into shared boxes.
    - Two round trips (GET /default-shell first, then POST /sessions): clean separation but adds latency and a new endpoint we'd have to teach the webui too.
    - Server-side resolve on empty argv (picked): zero new endpoints, the existing 400-on-empty-argv code path becomes the resolve point, and the right shell for the right user pops out automatically.
  - Trade-off: existing webui code that accidentally sends empty argv now spawns a shell instead of erroring. Acceptable — the webui always supplies `cmd`, and an accidental shell is more recoverable than an accidental 400.

- 2026-05-08T03:35Z — Decided **no `-l` and no `-i`** in the default shell argv.
  - Why: a PTY-attached shell auto-detects interactive via isatty(stdin/stdout), and `-l` re-runs `~/.zprofile` / `~/.bash_profile` on top of an already-inherited environment — measurable startup lag plus occasional duplicate $PATH entries. `ssh <host>` and `tmux new-window` both default to non-login interactive too; copying the convention.
  - Escape hatch: `hoot run -- bash -l` for users who want login behavior.
