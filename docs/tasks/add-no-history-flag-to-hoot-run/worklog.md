---
status: done
section: Add --no-history flag to hoot run
slug: add-no-history-flag-to-hoot-run
mode: worktree
spec:
created: 2026-05-09T09:53:40Z
---

> ## Add --no-history flag to hoot run
>
> ---
> status:
>   type: open
> ---
>
> Add a `--no-history` flag that skips scrollback history replay on attach. Only restore extras (kitty kbd state, modes, etc.) and the current screen snapshot — no scrollback bytes.
>
> Use case: attaching to a long-running session where replaying minutes of history is noisy/slow; you just want the live state.
>
> The flag is on `hoot run` per the request — it sets the session's default attach behavior. Whether to also expose a `hoot attach --no-history` override (so different attaches can choose independently) is the agent's call; flag the trade-off in the commit message either way.
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] add `NoHistory bool` to `SessionState` (state.json schema)
- [x] thread `NoHistory` through `SessionConfig` → Runner → `attachHandler` (skip scrollback bytes when true)
- [x] add `--no-history` flag to `hoot run`; pass via `spawnSpec` → `hoot __session --no-history`
- [x] propagate `NoHistory` through remote `createSessionReq` for `hoot run --remote --no-history`
- [x] tests: state.json round-trip, attach skips scrollback, run flag plumbing (local + remote)
- [x] update README usage block + add a short note about the flag
- [x] run full `go test ./...`; collect transcript for evidence

## Agent log

- 2026-05-09 — Landed the flag in one commit (4fe962f): SessionState schema bit, attach handler suppression, `hoot run` + `__session` plumbing, remote `createSessionReq` field, fake-hoot test helper update, plus targeted tests at both layers. Decided against a parallel `hoot attach --no-history` override; trade-off captured in commit body.

## Boss log

## Evidence

Commit: `4fe962f` on `add-no-history-flag-to-hoot-run` (worktree at
`~/github.com/hayeah/hootty/.worktrees/000`).

### Targeted tests — `go test -count=1 -run "..." -v ./...`

```
=== RUN   TestAttachHandlerNoHistorySendsEmptyScrollback
=== RUN   TestAttachHandlerNoHistorySendsEmptyScrollback/history-included
=== RUN   TestAttachHandlerNoHistorySendsEmptyScrollback/history-skipped
--- PASS: TestAttachHandlerNoHistorySendsEmptyScrollback (0.01s)
    --- PASS: TestAttachHandlerNoHistorySendsEmptyScrollback/history-included (0.00s)
    --- PASS: TestAttachHandlerNoHistorySendsEmptyScrollback/history-skipped (0.01s)
PASS
ok      github.com/hayeah/hootty        0.216s
=== RUN   TestCmdRunNoHistoryRecordsState
--- PASS: TestCmdRunNoHistoryRecordsState (0.54s)
=== RUN   TestCmdRunNoHistoryDefaultFalse
--- PASS: TestCmdRunNoHistoryDefaultFalse (0.11s)
=== RUN   TestCmdRunRemoteForwardsNoHistory
--- PASS: TestCmdRunRemoteForwardsNoHistory (0.00s)
PASS
ok      github.com/hayeah/hootty/cmd/hoot       0.961s
```

The session-package end-to-end test drives serveOne against a real
LibghosttyPTY whose scrollback ring has been pre-loaded with twelve
lines. Asserts: with `noHistory=false`, MsgSnapshotScrollback carries
non-empty bytes containing `line-`; with `noHistory=true`, the same
frame arrives but with a zero-byte payload (phase marker preserved).
The visible-screen frame stays non-empty in both runs and contains
`marker`, proving only history replay is suppressed.

The CLI tests verify both ends of the plumbing: the local path forks
`hoot __session --no-history` (the fake-hoot helper records the bit
into state.json), and the remote path serializes `no_history: true`
into `createSessionReq` — without that the user's intent would be
silently dropped at the wire.

### Full suite — `go test -count=1 ./...`

```
ok      github.com/hayeah/hootty                       7.027s
ok      github.com/hayeah/hootty/cmd/hoot             10.435s
?       github.com/hayeah/hootty/internal/attachetest  [no test files]
?       github.com/hayeah/hootty/internal/attachwire   [no test files]
ok      github.com/hayeah/hootty/internal/bootstrap    0.321s
ok      github.com/hayeah/hootty/internal/sessionpick  0.761s
ok      github.com/hayeah/hootty/internal/shortid      0.595s
ok      github.com/hayeah/hootty/internal/sshtransport 0.986s
```

### Smoke test — real binary against a tmpdir

`go build -o /tmp/hoot-nh ./cmd/hoot`, then:

```
$ /tmp/hoot-nh run --state-dir /tmp/hoot-nh.ZDPv4E --key smoke-nh \
    --no-history -- bash -lc 'for i in 1..8; do echo "history-line-$i"; done; sleep 1'
hoot: session "smoke-nh" started (state-dir=/tmp/hoot-nh.ZDPv4E)
smoke-nh

$ jq '.session.no_history' /tmp/hoot-nh.ZDPv4E/smoke-nh/state.json
true
```

Control run without `--no-history` writes state.json with the field
absent (`omitempty`), confirming the default is preserved.

`/tmp/hoot-nh run --help` lists the new flag in the usage line.

## Trouble report

Nothing surprising. The fake-hoot shell helper had to learn one new
case branch for `--no-history`; everything else was straight
threading. No flaky tests, no rebase, no design spirals.
