---
status: done
section: Hoot fuzzy picker: alive sessions only by default
slug: hoot-fuzzy-picker-alive-sessions-only-by-default
mode: worktree
spec:
created: 2026-05-08T10:01:54Z
---

> ## Hoot fuzzy picker: alive sessions only by default
>
> ---
> status:
>   type: open
> ---
>
> The target fuzzy matcher used by `hoot attach`, `hoot log`, and `hoot detach` currently includes dead sessions. For attach/detach those entries are dead-ends; for log they're surprising noise in the picker.
>
> Filter the picker to alive sessions only by default. Mirror the `--all` opt-in convention that landed for `hoot list` in `40fbb49` for verbs where opting back in makes sense — `hoot log --all` is the obvious one (dead-session logs are still readable). attach/detach probably don't need `--all` since dead sessions can't be attached.
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] Add `AliveOnly` field to `pickerOptions` and filter `states` in `resolveSessionKey`
- [x] Wire `attach` to set `AliveOnly: true` (no `--all` — dead can't attach)
- [x] Wire `detach` to set `AliveOnly: true`
- [x] Wire `kill` to set `AliveOnly: true`
- [x] Wire `clone` to set `AliveOnly: true` (no flag — dead-source clone via `--strict` if needed)
- [x] Add `--all` flag to `hoot log`; pass `AliveOnly: !*all`
- [x] Add tests covering filter behavior + `log --all` flow
- [x] Update README: `hoot log --all`, default-alive picker note
- [x] `go test ./...` clean

## Agent log
- 2026-05-08T10:07Z ack: sweeping all picker-using verbs. Plan: attach/detach/kill = AliveOnly=true (no flag); log + clone = --all opt-in (clone-from-dead is the recovery case).
- 2026-05-08T10:08Z ack: dropping --all from clone. Only log gets --all; fzf ergonomic is alive-only across the board, dead reachable via --strict.
- 2026-05-08T10:09Z landed picker filter wiring across attach/detach/kill/clone + log --all (b855361)
- 2026-05-08T10:11Z tests passing across full suite (aacffd3). picker filter + log --all covered.

## Boss log
- 2026-05-08T10:07Z follow-up clarification from the human: "attach, log, detach, etc." — sweep ALL verbs that use the target fuzzy picker, not just those three. If you find other picker-using verbs (e.g. anything that resolves a session id from user input via `internal/sessionpick`), apply the same alive-only default + `--all` opt-in convention where it makes sense.

## Evidence

### What changed

Filter the candidate session list passed to id-prefix resolution and the
fzf picker by alive flag. Set per-verb in each command's
`resolveSessionKey` call:

- `attach`, `detach`, `kill`, `clone` → `AliveOnly: true`. Dead sessions
  are dead-ends, so the picker ergonomic only applies to live ones.
  Dead targets remain reachable via `--strict <id>`.
- `log` → `AliveOnly: !*all`. New `--all` flag opts back in so the
  fuzzy picker covers exited sessions whose recordings are still on
  disk.
- `--strict` is unaffected anywhere — it goes through `store.Resolve`
  on the on-disk set, not the picker pipeline.

Commits on branch `hoot-fuzzy-picker-alive-sessions-only-by-default`:

- `b855361` hoot: filter the fzf picker to alive sessions by default
- `aacffd3` hoot: tests for alive-only picker filter + log --all
- `5f6873a` README: document alive-only picker default + hoot log --all

### Test output

`go test ./...` from the worktree:

```
ok  	github.com/hayeah/hootty	7.083s
ok  	github.com/hayeah/hootty/cmd/hoot	8.999s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.915s
ok  	github.com/hayeah/hootty/internal/shortid	1.210s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.656s
```

New tests for this section, all passing
(`go test ./cmd/hoot/ -run "TestResolveSessionKey_AliveOnly|TestCmdLog_All"`):

```
=== RUN   TestResolveSessionKey_AliveOnlyHidesDeadFromPicker
--- PASS: TestResolveSessionKey_AliveOnlyHidesDeadFromPicker (0.30s)
=== RUN   TestResolveSessionKey_AliveOnlyAllDeadIsErr
--- PASS: TestResolveSessionKey_AliveOnlyAllDeadIsErr (0.00s)
=== RUN   TestResolveSessionKey_AliveOnlyIDPrefixSkipsDead
--- PASS: TestResolveSessionKey_AliveOnlyIDPrefixSkipsDead (0.31s)
=== RUN   TestResolveSessionKey_AliveOnlyStrictBypassesFilter
--- PASS: TestResolveSessionKey_AliveOnlyStrictBypassesFilter (0.00s)
=== RUN   TestResolveSessionKey_AliveOnlyFalseStillShowsDead
--- PASS: TestResolveSessionKey_AliveOnlyFalseStillShowsDead (0.29s)
=== RUN   TestCmdLog_AllFlagFlipsAliveOnly
--- PASS: TestCmdLog_AllFlagFlipsAliveOnly (0.30s)
PASS
```

Coverage of the user-visible matrix:

- `AliveOnlyHidesDeadFromPicker` — fzf stdin contains only the live key,
  the dead key is filtered out (asserted by inspecting captured stdin).
- `AliveOnlyAllDeadIsErr` — empty post-filter returns exit 2 with the
  "no live sessions" message hinting at `--all`.
- `AliveOnlyIDPrefixSkipsDead` — id-prefix fast path doesn't
  short-circuit on a dead-only match; falls through to fzf, which
  doesn't see the dead session either, surfacing the typed pattern in
  the error.
- `AliveOnlyStrictBypassesFilter` — `--strict` still resolves a dead
  on-disk key even when `AliveOnly` is set.
- `AliveOnlyFalseStillShowsDead` — negative control: with the field
  unset (default), dead sessions remain in the picker.
- `TestCmdLog_AllFlagFlipsAliveOnly` — exercises `cmdLog` end-to-end
  on a state-dir holding a single dead recording. Default invocation
  errors with "no live sessions"; the same invocation with `--all`
  reads the recording and prints the recorded plain text.

The existing `TestResolveSessionKey_*` tests for the verbs (clone,
kill, detach) all continue to pass — they don't set `AliveOnly`, so
the default behavior is unchanged at the function level. The
per-verb wiring is exercised by `TestCmdLog_AllFlagFlipsAliveOnly`
for log; the analogous wiring on attach/detach/kill/clone is a
single-line literal change reviewable by inspection.

## Trouble report

- master is in a slightly broken state on the user's main checkout —
  `cmd/hoot/attach_picker_test.go` is staged in the index as a stale
  duplicate of `cmd/hoot/picker_test.go` (`fakeFZF redeclared`). The
  worktree branched from `8c658cc` which is clean, so this didn't
  affect the work, but leaving a note since the user may want to
  unstage that file.
- The post-filter "no live sessions" error path uses `len(states) == 0`
  *after* the alive filter, so a state-dir with sessions but none alive
  produces a different message than a fully-empty dir. That's
  intentional (the message hints at `--all`), but worth flagging in
  case the user prefers a single message.
