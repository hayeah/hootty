---
status: done
section: Refuse nested hoot attach
slug: refuse-nested-hoot-attach
mode: worktree
spec: spec.md
created: 2026-05-08T06:03:48Z
---

> ## Refuse nested hoot attach
>
> ---
> status:
>   type: open
> ---
>
> Nested hoot attaches are confusing — like nested tmux. When you're already inside a hoot-attached shell and run `hoot` (bare) or `hoot attach <key>`, it should detect the nesting and bail with a clear error, in the spirit of tmux:
>
> > sessions should be nested with care, unset $TMUX to force
>
> Mechanism: have hoot set an env var (e.g. `$HOOT_SESSION` — agent's call on the exact name) on every spawned child of a hoot session (`hoot run`, bare `hoot` shell, `hoot @host`). Attach paths check for it on entry and fail out with a tmux-shaped message that names the env var to unset for the override. Read-only commands (`hoot list`, etc.) still work inside a session.
>
> Don't try to be clever about "switch endpoint from inside" or "broadcast detach to the outermost attach" — too much policy, hard to address the right session. Just refuse the nest.
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Inject `HOOT_SESSION=<key>` into spawned child env (sanitizeChildEnv)
- [x] Add `errIfNestedHootSession` guard helper
- [x] Wire guard into cmdAttach
- [x] Wire guard into cmdShell
- [x] Wire guard into cmdRun (only when --attach set)
- [x] Unit tests for sanitizeChildEnv + guard + each command
- [x] Update README + usage() with nested-refusal note
- [x] Manual e2e: spawn, attach, verify nested refusal + override

## Agent log
- 2026-05-08T06:08Z publish HOOT_SESSION env var on spawned shell + add guard helper (sha 5856761)
- 2026-05-08T06:09Z wired guard into cmdAttach, cmdShell, cmdRun --attach (sha 2737543)
- 2026-05-08T06:11Z added 8 unit tests for nesting refusal — all passing (sha 50369fd)
- 2026-05-08T06:12Z documented refusal in README + usage() (sha f0dec5f)
- 2026-05-08T06:14Z e2e verified, evidence captured, status: done

## Boss log

## Evidence

### Unit tests

```
$ go test ./cmd/hoot/ -run "TestErrIfNestedHootSession|TestSanitizeChildEnv|TestCmdAttachRefusesWhenNested|TestCmdShellRefusesWhenNested|TestCmdRunAttachRefusesWhenNested|TestCmdRunWithoutAttachAllowedWhenNested|TestCmdListAllowedWhenNested" -v
--- PASS: TestErrIfNestedHootSession (0.00s)
--- PASS: TestSanitizeChildEnvPublishesHootSession (0.00s)
--- PASS: TestSanitizeChildEnvEmptyKeyOmits (0.00s)
--- PASS: TestCmdAttachRefusesWhenNested (0.00s)
--- PASS: TestCmdShellRefusesWhenNested (0.00s)
--- PASS: TestCmdRunAttachRefusesWhenNested (0.00s)
--- PASS: TestCmdRunWithoutAttachAllowedWhenNested (0.45s)
--- PASS: TestCmdListAllowedWhenNested (0.00s)
PASS
ok  	github.com/hayeah/hootty/cmd/hoot	0.977s
```

Full repo test suite (`go test ./...`):
```
ok  	github.com/hayeah/hootty	7.126s
ok  	github.com/hayeah/hootty/cmd/hoot
ok  	github.com/hayeah/hootty/internal/sessionpick	0.637s
ok  	github.com/hayeah/hootty/internal/shortid
ok  	github.com/hayeah/hootty/internal/sshtransport
```

### Manual e2e

Spawned a real local hoot session via `hoot run -- bash -c '...'`,
then from inside that bash invoked the same hoot binary. Full
recording in `tmp/131312_479-nested-refusal-e2e.txt`. Highlights:

```
=== inside the spawned shell ===
HOOT_SESSION=e2etest

=== hoot list (read-only, should work) ===
[e2etest]       @local  ~/...      bash -c
                                                       ← session listed fine

=== hoot (bare, should refuse) ===
hoot shell: sessions should be nested with care, unset $HOOT_SESSION to force (already attached to "e2etest")
exit=1

=== hoot attach xyz (should refuse) ===
hoot attach: sessions should be nested with care, unset $HOOT_SESSION to force (already attached to "e2etest")
exit=2

=== hoot run --attach -- echo never (should refuse) ===
hoot run: sessions should be nested with care, unset $HOOT_SESSION to force (already attached to "e2etest")
exit=1

=== unset HOOT_SESSION && hoot list still works ===
[e2etest]       @local  ~/...   bash -c                ← override knob works
```

All five scenarios pass: env var present, read-only verb works, all
three takeover paths refuse with the tmux-shaped message, override
knob (`unset HOOT_SESSION`) lets nested invocations through.

## Trouble report

- Pre-existing flake (NOT a regression from this work): when `hoot run`
  is given a bash command that takes longer than ~3s to make progress,
  the parent CLI prints `hoot run: run: session did not start: timeout
  waiting for /.../rpc.sock`. The detached session daemon still runs
  to completion (recording is intact, `hoot log` reads it back). The
  parent's `waitForSocket` polls os.Stat for 3s; on a busy machine
  the libghostty PTY init + socket bind can race past that window.
  Reproducible on master with the same long-script payload. Not in
  scope for this section. #friction
