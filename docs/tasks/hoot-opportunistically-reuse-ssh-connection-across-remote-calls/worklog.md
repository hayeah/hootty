---
status: done
section: hoot: opportunistically reuse ssh connection across remote calls
slug: hoot-opportunistically-reuse-ssh-connection-across-remote-calls
mode: worktree
spec: spec.md
created: 2026-05-06T04:12:07Z
---

> ## hoot: opportunistically reuse ssh connection across remote calls
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Today every `hoot --remote ssh://devbox <cmd>` invocation likely opens a fresh ssh connection (modulo whatever ControlMaster does for free). For one-shot calls (`list`, `resolve`, `run --detach`), that's fine — let the connection die after the call.
>
> But for the case where a **long-running `hoot attach`** already has a healthy ssh connection to the same host, subsequent commands on the same host should **opportunistically reuse** that existing connection instead of opening a new ssh process.
>
> ### Goal
>
> `hoot --remote ssh://devbox attach foo` (long-running) is up. In another terminal, `hoot --remote ssh://devbox list` finishes via the existing ssh connection — fast, no extra auth, no extra ControlMaster spawn.
>
> ### Open questions for the agent (do something reasonable, justify in spec.md / agent log)
>
> - **What does "the existing connection" mean concretely?** OpenSSH ControlMaster already gives us session-level multiplexing through `ControlPath`. The first `hoot --remote ssh://devbox` boots a master; subsequent ones with the same `ControlPath` reuse it transparently. That may already be happening — verify before designing anything new. If yes, the work might be just "ensure the ControlPath is consistent across invocations".
> - **Per-channel vs per-process**: ssh multiplexes channels within a connection. Each command is a new channel; the connection stays open. Is the unit of "reuse" the ssh process (cheap-once-it's-up) or something finer? For HTTP Upgrade over SSH (the attach path), each attach already gets its own channel inside the same ssh connection — so reuse may already be free for that path.
> - **One-shot fallthrough**: if no long-running attach is up, `hoot list` against ssh:// should still work standalone. Don't make one-shots depend on a pre-existing attach.
> - **Lifecycle**: if the attach process exits, what happens to in-flight one-shots that are using its connection? If ControlMaster, the master stays up until idle timeout — non-issue. If something custom, spec the cleanup.
> - **Detection**: how does a fresh `hoot list --remote ssh://devbox` know there's an existing connection it can use? File-based marker (the ControlPath sock from the running attach), or plain reuse-by-ControlPath-convention?
>
> ### Recommended angle
>
> Before writing anything new, **verify whether OpenSSH ControlMaster with a stable hashed ControlPath already gives us this for free** — the `--remote` work landed at `cc30819` set up `ControlMaster` + hashed `ControlPath`. If `hoot list --remote ssh://devbox` while an attach is running already short-circuits to the master mux, the answer might be "no code change, just tighten the ControlPath consistency or document the behavior."
>
> If ControlMaster is *not* doing this transparently (e.g. the attach uses a tunnel/forward but the one-shot doesn't reuse the master), find why and fix it. Smallest viable change.
>
> ### Procedure
>
> - [ ] verify current behavior, identify the gap (or confirm no gap exists), ship a minimal fix or docs
>   - evidence: while a long-running `hoot attach --remote ssh://devbox <key>` is up, `time hoot list --remote ssh://devbox` is materially faster than the cold first call (no fresh ssh handshake / auth)
>   - evidence: when no attach is up, one-shots still work fine
>   - evidence: writeup of what was happening before vs after the fix (or "already worked, here's how")
>
> Use codex.

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Check out `github.com/hayeah/hootty`, inspect SSH remote implementation, and write workspace `spec.md`
- [x] Build a worktree `hoot` binary
- [x] Run focused SSH transport / remote command tests before changes
- [x] Verify SSH behavior with and without a long-running attach, saving transcript under `tmp/`
- [x] Add the smallest regression tests/docs or code fix based on the verification gap
- [x] Run final tests
- [x] Commit repo changes and fill evidence

## Agent log

- 2026-05-06T04:21Z - Checked out `github.com/hayeah/hootty` via `boss checkout`; wrote `spec.md`. Initial read shows `internal/sshtransport` already uses stable hashed `ControlPath` with `ControlMaster=auto` and `ControlPersist=60`; next is build/tests/manual verification.
- 2026-05-06T04:23Z - Local baseline passed: `make build`; `go test ./internal/sshtransport ./cmd/hoot`.
- 2026-05-06T04:43Z - Landed hootty commit `6e49d5f`: short fallback dirs for long OpenSSH socket paths, bounded tunnel cleanup, regression tests, and README clarification of ControlMaster reuse.

## Boss log

## Evidence

Commit on `github.com/hayeah/hootty` branch `hoot-opportunistically-reuse-ssh-connection-across-remote-calls`:

- `6e49d5f` Stabilize ssh remote socket reuse

Tests:

```text
$ go test -timeout 30s ./internal/sshtransport ./cmd/hoot
ok  	github.com/hayeah/hootty/internal/sshtransport	0.697s
ok  	github.com/hayeah/hootty/cmd/hoot	2.757s

$ go test -timeout 60s ./...
ok  	github.com/hayeah/hootty	0.708s
ok  	github.com/hayeah/hootty/cmd/hoot	2.252s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	0.785s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.969s
```

Manual SSH smoke transcript:

- `tmp/ssh-localhost-reuse-smoke.txt`
- Used `ssh://localhost` because `ssh://devbox` requires a Tailscale web-auth check in this environment.
- The smoke used a long workspace `--state-dir` to exercise the new short socket fallback.
- Standalone one-shot with no attach active succeeded:

```text
## standalone list with no attach active
real 0.67
standalone_exit=0
control_check_after_standalone=Master running (pid=16697)
master_count_after_standalone=1
```

- While `hoot attach --remote ssh://localhost reuse112157` was running, a second process ran `hoot list --remote ssh://localhost` through the same master pid:

```text
## list while attach is active
control_check_before_warm_list=Master running (pid=16697)
real 0.25
warm_list_exit=0
warm_list_stdout_lines=1
control_check_after_warm_list=Master running (pid=16697)
```

- Process evidence while attach was active shows one persistent OpenSSH mux master plus the attach's short-lived tunnel client using the same `ControlPath`; the warm `list` did not spawn a second master:

```text
16697 ... ssh: /Users/me/Library/Caches/hootty/ssh-control/cm-49960de5 [mux]
17321 ... ssh -o ControlMaster=auto -o ControlPath=/Users/me/Library/Caches/hootty/ssh-control/cm-49960de5 ...
```

- After detaching, one-shots still worked standalone:

```text
## list after attach detached
real 0.25
post_attach_list_exit=0
post_attach_stdout_lines=1
control_check_after_detach=Master running (pid=16697)
```

`ssh://devbox` host-specific transcript:

- `tmp/ssh-reuse-smoke-short-state.txt`
- Blocked by Tailscale auth, not by hoot after the socket path fix:

```text
hoot list: Get "http://hoot/sessions": resolve remote home: exit status 255: # Tailscale SSH requires an additional check.
# To authenticate, visit: https://login.tailscale.com/a/l104ddedc32fa1c
Connection to 100.72.239.6 port 22 timed out
```

## Trouble report

- The first smoke used the workspace path as `--state-dir` and exposed two OpenSSH Unix socket path failures before real connection reuse could be tested:
  - `ControlPath too long (...) >= 104 bytes`
  - `Bad local forwarding specification '<long workspace path>/fwd-...sock:127.0.0.1:<port>'`
- `ssh://devbox` could not be used for final timing evidence because Tailscale SSH requires a browser auth check. I used `ssh://localhost` for same-transport evidence and kept the failed devbox transcript in `tmp/`.
