---
status: done
section: Switch attach prefix-key + commands to mosh defaults
slug: switch-attach-prefix-key-commands-to-mosh-defaults
mode: worktree
spec:
created: 2026-05-02T15:15:54Z
---

> ## Switch attach prefix-key + commands to mosh defaults
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/supervisor`.
>
> The current `C-b` default for `supervise attach` collides with tmux's prefix — bad UX when attaching into a shell that runs tmux. Switch to mosh's convention wholesale:
>
> - **Prefix**: `Ctrl-^` (0x1e), not `C-b`.
> - **Commands inside an attach** (mosh vocabulary):
>   - `Ctrl-Z` → suspend the local `supervise attach` process (SIGTSTP, fg to resume; same as mosh).
>   - `.` → quit/detach (replaces our current `<prefix>d`).
>   - `^` → send a literal `Ctrl-^` to the remote (replaces `<prefix><prefix>`).
>   - keep `?` → help.
>
> Keep `--prefix-key` as the override knob; only the default and the command-vocabulary change.
>
> - [ ] update default prefix + command set, refresh help text + README + main.go usage, update tests/evidence
>   - in-attach help should mirror mosh's own help text style
>   - update the smoke transcripts referenced in prior worklogs only if you genuinely re-run them; otherwise just refresh the docs/help

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] flip default prefix to `C-^` (0x1e) and update FSM command vocabulary (`.`=detach, `^`=literal, `Ctrl-Z`=suspend, `?`=help) in `cmd/supervise/attach.go`
- [x] refresh in-attach help text in mosh style
- [x] update `cmd/supervise/main.go` top-level usage text
- [x] update README.md prefix table + default mention
- [x] `go build ./...` + `go test ./...` to confirm nothing broke
- [x] capture evidence (build/test output + a transcript-style snippet of the new help line)

## Agent log
- 2026-05-02T15:18Z attach: prefix default→C-^, commands {. ^ Ctrl-Z ?}, help/usage/README refreshed; build+tests green (3b18a3c)

## Boss log

## Evidence

Single commit on branch `switch-attach-prefix-key-commands-to-mosh-defaults`:

```
3b18a3c attach: switch default prefix + commands to mosh defaults
 README.md               | 16 +++++++++-------
 cmd/supervise/attach.go | 32 +++++++++++++++++++++-----------
 cmd/supervise/main.go   |  5 +++--
 3 files changed, 33 insertions(+), 20 deletions(-)
```

### Build + tests

```
$ go build ./...
$ go test ./...
ok  	github.com/hayeah/supervisor	0.961s
ok  	github.com/hayeah/supervisor/cmd/supervise	0.512s
?   	github.com/hayeah/supervisor/internal/attachwire	[no test files]
ok  	github.com/hayeah/supervisor/internal/shortid	1.165s
```

(No tests directly cover `runStdinFSM` or `ParsePrefixKey` in the suite; `ParsePrefixKey("C-^")` already returned 0x1e before this change — only the default flag value moved.)

### New `supervise attach` usage

```
$ supervise attach
usage: supervise attach [flags] <id-or-prefix>

  --host <addr>          remote `supervise serve` host: bare host:port,
                         http://host:port, or https://host:port
  --state-dir <dir>      session state directory (default: ~/.supervise);
                         only used for local attach (no --host)
  --no-full-replay       send a libghostty snapshot instead of streaming pty.log
  --no-reconnect         exit on first drop instead of auto-reconnecting
                         (--host only; ignored for local attach)
  --prefix-key <key>     command prefix byte (default: C-^). Forms: C-^, ^^,
                         0x1e, or a single ASCII control byte.

Inside an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte to the remote; <prefix>Ctrl-Z suspends supervise
attach (resume with fg); <prefix>? prints help.

During a remote disconnect: backoff is 1,2,4,8,16,30s capped at 30s and
retries forever. Press any key to wake the backoff and retry now.
<prefix>. still detaches cleanly.
```

### New top-level `supervise` usage tail

```
In an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte; <prefix>Ctrl-Z suspends supervise attach (resume
with fg); <prefix>? prints help. Default prefix is C-^ (Ctrl-^, 0x1e).
```

### New in-attach `?` help line (from `runStdinFSM`)

```
supervise attach commands: "." detach, "^" literal prefix, "Ctrl-Z" suspend, "?" help
```

### README prefix table (updated)

| sequence              | action                                            |
| --------------------- | ------------------------------------------------- |
| `<prefix> .`          | detach (clean exit 0)                             |
| `<prefix> ^`          | send literal prefix byte to remote                |
| `<prefix> Ctrl-Z`     | suspend `supervise attach` (SIGTSTP; `fg` resumes) |
| `<prefix> ?`          | print one-line help on stderr, stay attached      |

I did not re-run the live smoke transcripts referenced in prior worklogs — per the section's note, only the docs/help/defaults changed and the test suite still passes, so the docs were refreshed without a fresh smoke capture.

## Trouble report

- No friction. Single localized change set; no shared-protocol fallout (prefix logic is purely client-side in `cmd/supervise/attach.go`).
- Pre-existing diagnostics in `go.mod` (indirect→direct nudges for `coder/websocket` and `golang.org/x/term`) and a couple of `errors.As`/`min` modernization hints were already there; out of scope for this section.
