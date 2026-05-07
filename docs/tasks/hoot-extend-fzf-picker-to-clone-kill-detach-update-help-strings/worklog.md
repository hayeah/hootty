---
status: done
section: Hoot — extend fzf picker to clone/kill/detach + update help strings
slug: hoot-extend-fzf-picker-to-clone-kill-detach-update-help-strings
mode: worktree
spec:
created: 2026-05-07T06:22:33Z
---

> ## Hoot — extend fzf picker to clone/kill/detach + update help strings
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`. Direct implementation (boss todo, not spec mode). The picker landed in commit `e93c9cc` for `hoot attach`; this section extends the same machinery to other subcommands that take an `<id-or-prefix>` and updates the `-h` strings.
>
> ### Goal
>
> Match `hoot attach`'s picker behavior for these subcommands:
>
> ```
> hoot clone   [--remote <url>] [--state-dir <d>] [--key <new-key>]
>                   [--env NAME[=VALUE]] [--env-file <path>]
>                   [--attach|--detach] <id-or-prefix>
> hoot kill    [--remote <url>] [--state-dir <d>] [-s SIG] <id-or-prefix>
> hoot detach  [--remote <url>] [--state-dir <d>] <session-prefix>[/<attachment-prefix>]
> ```
>
> (`hoot attach` already has it from `e93c9cc`. This section just extends + updates help text.)
>
> Behavior should mirror `hoot attach`:
>
> - No id arg + tty → drop into the fzf picker; resolve to a session id; continue.
> - Pattern arg that doesn't match an id-prefix exactly + tty → fall back to fzf fuzzy with the pattern preseeded; on unique match, attach the result; on multi, picker pre-seeded; on zero, exit 2.
> - Pattern arg + non-tty → 1 hit attach, 0/>1 exit 2 with candidates.
> - `--strict` flag → preserves today's hard id-prefix behavior; never opens picker.
>
> ### Specific notes per subcommand
>
> - **`hoot clone`**: positional id selects the *source* session to clone. Picker over alive sessions makes sense.
> - **`hoot kill`**: positional id selects the session to signal. Picker over alive sessions.
> - **`hoot detach`**: takes `<session-prefix>[/<attachment-prefix>]`. Two cases:
>   - No arg + tty → picker over sessions; once a session is picked, if it has multiple attachments, secondary picker over its attachments (or just close-all if the user picked a session-level entry).
>   - `<sess>` (no slash) + tty → as today, semantics is "close all attachments on that session". Picker only if `<sess>` doesn't resolve to a unique id.
>   - `<sess>/<att>` + tty → picker on the attachment level if `<att>` is a fuzzy pattern; do not switch sessions.
>   - Push back if you find a simpler shape — the boss is open. The minimum is "no-arg + tty → picker".
> - Reuse `internal/sessionpick` (`Format`, `IDResolve`) and `cmd/hoot/attach_picker.go` (`runFZFPicker`). Lift any shared resolve logic into `sessionpick` if needed; do not duplicate.
> - `runFZFPicker` is currently in `cmd/hoot/attach_picker.go` — if it becomes shared by multiple subcommands, consider renaming to `cmd/hoot/picker.go`. Judgment call.
>
> ### Help-text update
>
> The existing `-h` for each subcommand should:
> - Include a one-line note that bare invocation drops into a picker (when tty).
> - Mention `--strict` for hard-id semantics.
> - Not duplicate the long picker-line-format docs from `hoot attach -h` — point at it (e.g. "see `hoot attach -h` for the picker line format and fzf query vocabulary").
>
> ### Tests
>
> - For each new subcommand picker path: unit test via the same `/bin/sh` shim pattern from `attach_picker_test.go` (no real fzf in CI). Cover argv flags, stdin shape, preseed → `--query --select-1 --exit-0`, exit-code branches.
> - E2E smoke: spawn a couple of sessions, run each subcommand with no arg via expect-pty (or stub fzf), verify the routing matrix.
>
> ### Surface
>
> - [ ] implement and verify
>   - extend each subcommand's resolve path with picker fallback + `--strict` flag
>   - update `-h` for each
>   - update README's per-subcommand sections
>   - tests per-subcommand mirroring `attach_picker_test.go`
>   - evidence: `go test ./...` tail + smoke transcript hitting each subcommand's no-arg path

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

### Phase 1 — share resolve helper
- [x] Lift `resolveAttachKey`'s reusable shape into `cmd/hoot` (or `sessionpick`) — split into a `resolveSessionKey` that takes the session list + arg + strict + tty + no-arg-allowed; reuse from `attach`, `clone`, `kill`. Rename `attach_picker.go` → `picker.go` if appropriate.
- [x] Add a `--prompt`-able variant or `pickerOptions{prompt, header}` so each subcommand's picker shows a sensible prompt (`attach>`, `clone-source>`, `kill>`, `detach>`).

### Phase 2 — wire picker into clone, kill, detach
- [x] `hoot clone`: extend resolve path with picker fallback + `--strict`. Update `-h` text.
- [x] `hoot kill`: extend resolve path with picker fallback + `--strict`. Update `-h` text.
- [x] `hoot detach`: no-arg + tty → picker over alive sessions; `<sess>` (no slash) + tty + miss → picker; `<sess>/<att>` semantics keep existing attachment-prefix resolve. Add `--strict`. Update `-h`.

### Phase 3 — tests
- [x] Subcommand-level unit tests for each new picker path mirroring `attach_picker_test.go` (shim fzf, assert argv/stdin/preseed → exit code branches). Cover --strict.
- [x] Smoke transcript hitting no-arg path of clone, kill, detach (manual run with stub fzf).

### Phase 4 — README + verify
- [x] Update README per-subcommand sections (clone, kill, detach) — short note + cross-reference to `hoot attach -h`.
- [x] Update top-level CLI synopsis to show `--strict` on clone/kill/detach.
- [x] `go test ./...` clean. Capture tail.

## Agent log

- 2026-05-07T13:55Z Phase 1 landed in dfee6bc — picker.go owns runFZFPicker (now takes pickerOptions{Verb}) and resolveSessionKey/loadSessionList. attach.go calls resolveSessionKey with Verb="attach"; existing tests pass.
- 2026-05-07T13:54Z Phase 2 landed in 389fb4d — clone, kill, detach all call resolveSessionKey with their own Verb. --strict added on each. Help text rewritten to mention picker + cross-reference `hoot attach -h`. Existing TestCmdCloneRemote needed --strict so the test server (which only stubs the clone POST) doesn't see the new GET /sessions probe.
- 2026-05-07T13:57Z Phase 3 landed in 54e5cdd — picker_resolve_test.go: 9 tests, 13 subcases. Made stdoutIsTTY a package var so tests can fake tty=true. Verb-threaded-through-fzf has clone/kill/detach subtests so a regression in any one is visible.
- 2026-05-07T13:58Z Phase 4 landed in 9dbbde6 — README updated (per-subcommand examples + cross-ref to `hoot attach -h`), main.go usage shows --strict on clone/kill/detach. Smoke transcript captured at tmp/smoke-transcript.txt covering -h for each verb, no-arg picker invocation showing the picker line format and prompt, and the --strict / non-tty error paths. `go test ./...` all-green.

### Design decisions

- Detach picker shape: I went with the minimum the boss listed — no-arg + tty → picker over sessions; bare `<sess>` form gets picker fallback like attach; `<sess>/<att>` keeps strict attachment-prefix semantics (no secondary attachment picker).
  - The boss left this open. I dropped the secondary attachment picker because attachment ids are short-generated random shortids that the user typically copies from `hoot list`, not fuzzy-matchable terms; running fzf over them would add noise without value. If the user wants a specific attachment, they pass `<sess>/<att>` and it resolves strictly. Bare picker → close-all-attachments on the picked session is the obvious "I want to detach something, drop me in" path.
  - Trade-off: the user can't pick a specific attachment via the picker. If that turns out to matter, the addition is mechanical (fetch attachments[] from the picked session, run a second fzf with them as input).

## Boss log

## Evidence

### Commits on branch `hoot-extend-fzf-picker-to-clone-kill-detach-update-help-strings`

- `dfee6bc` Lift fzf picker resolve into resolveSessionKey/picker.go
- `389fb4d` Wire fzf picker into hoot clone, kill, detach + add --strict
- `54e5cdd` Test resolveSessionKey across attach/clone/kill/detach verbs
- `9dbbde6` Document fzf picker on clone/kill/detach in README

### `go test ./...` — full hootty test suite

```
ok  	github.com/hayeah/hootty	7.445s
ok  	github.com/hayeah/hootty/cmd/hoot	5.654s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.972s
ok  	github.com/hayeah/hootty/internal/shortid	0.334s
ok  	github.com/hayeah/hootty/internal/sshtransport	1.321s
```

Tail captured at `tmp/go-test-tail.txt`.

### Picker resolve tests (the new coverage)

```
$ go test ./cmd/hoot/ -run TestResolveSessionKey -v
=== RUN   TestResolveSessionKey_StrictRequiresArg
    --- PASS: attach / clone / kill / detach
=== RUN   TestResolveSessionKey_StrictResolvesPrefixLocally    PASS
=== RUN   TestResolveSessionKey_NoArgNonTTY                    PASS
=== RUN   TestResolveSessionKey_VerbThreadsThroughToFZF
    --- PASS: clone / kill / detach (each gets the right --prompt + --header)
=== RUN   TestResolveSessionKey_IDPrefixMissPreseedsFZF        PASS
=== RUN   TestResolveSessionKey_IDPrefixMatchSkipsPicker       PASS
=== RUN   TestResolveSessionKey_AmbiguousIDBeatsFuzzy          PASS
=== RUN   TestResolveSessionKey_PickerCancelMaps130            PASS
=== RUN   TestResolveSessionKey_FZFExit1NoMatchMaps2           PASS
PASS
```

### Smoke transcript (full file at `tmp/smoke-transcript.txt`)

`-h` snippet for each subcommand confirms the new "no-arg + tty drops
into picker / --strict for hard prefix" framing and the
`hoot attach -h` cross-reference:

```
$ hoot kill -h
usage: hoot kill [flags] [<id-or-prefix-or-pattern>]
... With no positional argument (and on a tty), an interactive fzf picker
selects the target session. ... See `hoot attach -h` for the picker
line format and fzf query vocabulary.
  --strict              exact id-prefix match only — no fzf, no picker
```

(clone -h and detach -h have parallel structure; full text in the file.)

`hoot kill` no-arg + simulated tty hits the picker; the shim captured
the per-verb prompt and the picker line format the user sees:

```
fzf stdin (the picker line format the user sees):
1	[src888]	@local	/work	nvim spec.md	(dead)
2	[src999]	@local	/tmp	bash -l	(dead)

fzf argv (prompt + verb + flags):
  --prompt=kill>
  --header=↑↓ select · enter kill · esc cancel
  --with-nth=2..
  --no-sort
  ...
```

Negative-path checks all return exit 2 with descriptive messages:

```
$ hoot detach --strict bogus       # picker bypassed, prefix invalid
hoot detach: no match for "bogus"
exit=2

$ hoot kill --strict               # --strict requires arg
hoot kill: --strict requires a session id
exit=2

$ hoot kill                        # no-arg + non-tty
hoot kill: no session id given; tty required for picker
exit=2

$ hoot kill zzzz                   # arg + non-tty + miss
hoot kill: no session matched "zzzz" (tty required for picker)
exit=2
```

## Trouble report

- TestCmdCloneRemote in clone_test.go used to assume the only HTTP
  call was POST /sessions/{key}/clone. After the picker wiring,
  cmdClone first issues GET /sessions to load the candidate list for
  client-side IDResolve (and picker fallback). Fix was to add
  `--strict` to the test invocation, since the test isn't checking
  the picker — it's checking the clone wire shape. Recorded as a
  one-line tweak in the same commit.

- Decided not to add a secondary attachment picker for
  `hoot detach <sess>/<att>` even though the section text floated it
  as an option. Attachment ids are short generated random shortids
  (not human-readable), so fuzz-matching would add noise without
  value. The boss explicitly left this as a judgment call. If it ever
  matters, the addition is mechanical: fetch the picked session's
  `attachments[]` from `state.json` and run a second fzf with them as
  input.

- gopls (the LSP backing the editor) repeatedly reported "could not
  import github.com/hayeah/hootty (cannot find package in GOROOT)"
  for files inside the worktree at `.worktrees/001/`. The actual
  `go build ./...` and `go test ./...` invocations succeeded — gopls
  just doesn't index sibling worktrees of the same repo without a
  `go.work` file. Ignored throughout. #friction

