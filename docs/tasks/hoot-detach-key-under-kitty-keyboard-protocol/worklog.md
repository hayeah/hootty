---
status: done
section: Hoot detach key under kitty keyboard protocol
slug: hoot-detach-key-under-kitty-keyboard-protocol
mode: worktree
spec:
created: 2026-05-06T09:02:54Z
---

> ## Hoot detach key under kitty keyboard protocol
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Spec: `/Users/me/Dropbox/notes/2026-05-06/hoot-detach-key-kitty-keyboard-protocol_claude.md` — read it end-to-end before starting. Summary: hoot's `C-^ .` detach chord is ignored under Ghostty/cmux when Claude Code enables the kitty keyboard protocol, because the outer terminal re-encodes the chord as CSI u sequences that `runStdinFSM` (`cmd/hoot/attach.go:582`) does not match. Plan in the note is option 1 — extend the FSM to also accept CSI u encodings of the prefix key and chord follow-up.
>
> - [ ] implement per spec and verify
>   - follow the implementation sketch + edge cases in the note
>   - add the unit tests listed under "Tests to add"
>   - evidence: test output for each enumerated case + a manual smoke-test note (Ghostty + Claude Code → detach works)

## Todos

- [x] factor runStdinFSM into a testable FSM core driven by a `readByte` function
- [x] add CSI u key-event parser and prefix/chord matchers
- [x] wire kitty CSI u acceptance into the FSM (prefix + chord follow-up)
- [x] unit tests covering all spec cases (legacy, CSI u variants, alternate-key form, non-prefix CSI u)
- [x] `go test ./...` passing in the worktree
- [ ] manual smoke test under Ghostty + Claude Code — pending human verification (recipe in Evidence)

## Agent log
- 2026-05-06T09:09Z FSM core refactor + CSI u prefix/chord matching landed; full test suite green (5373109)
- 2026-05-06T09:10Z status: done — unit-test evidence + smoke-test recipe in worklog; manual Ghostty+Claude check left to human

## Boss log

## Evidence

### Unit tests (commit 5373109)

`go test ./cmd/hoot/ -run 'TestRunChordFSM|TestParseCSIuParams|TestIsPrefixKey' -v`:

```
=== RUN   TestRunChordFSM_LegacyDetach
--- PASS: TestRunChordFSM_LegacyDetach (0.00s)
=== RUN   TestRunChordFSM_KittyShifted
--- PASS: TestRunChordFSM_KittyShifted (0.00s)
=== RUN   TestRunChordFSM_KittyBase
--- PASS: TestRunChordFSM_KittyBase (0.00s)
=== RUN   TestRunChordFSM_KittyAlternate
--- PASS: TestRunChordFSM_KittyAlternate (0.00s)
=== RUN   TestRunChordFSM_KittyPrefixUnknownFollowup
--- PASS: TestRunChordFSM_KittyPrefixUnknownFollowup (0.00s)
=== RUN   TestRunChordFSM_NonPrefixCSIu
--- PASS: TestRunChordFSM_NonPrefixCSIu (0.00s)
=== RUN   TestRunChordFSM_NonCSIuEscapeForwarded
--- PASS: TestRunChordFSM_NonCSIuEscapeForwarded (0.00s)
=== RUN   TestRunChordFSM_LegacyAndKittyMixed
--- PASS: TestRunChordFSM_LegacyAndKittyMixed (0.00s)
=== RUN   TestParseCSIuParams
--- PASS: TestParseCSIuParams (0.00s)
=== RUN   TestIsPrefixKey
--- PASS: TestIsPrefixKey (0.00s)
=== RUN   TestRunChordFSM_OtherChordsCSIu
--- PASS: TestRunChordFSM_OtherChordsCSIu (0.00s)
=== RUN   TestRunChordFSM_CloneError
--- PASS: TestRunChordFSM_CloneError (0.00s)
PASS
ok  	github.com/hayeah/hootty/cmd/hoot	0.225s
```

Mapping from spec's "Tests to add" → test names:

- legacy `0x1E .` → detach: `TestRunChordFSM_LegacyDetach`
- `\e[94;6u \e[46u` → detach: `TestRunChordFSM_KittyShifted`
- `\e[54;6u \e[46u` → detach: `TestRunChordFSM_KittyBase`
- `\e[94:54;6u \e[46u` → detach: `TestRunChordFSM_KittyAlternate`
- `\e[94;6u <other>` → no detach: `TestRunChordFSM_KittyPrefixUnknownFollowup`
- non-prefix CSI u (`\e[97;5u` ≈ `C-a` when prefix is `C-^`) → forward verbatim: `TestRunChordFSM_NonPrefixCSIu`

Additional coverage beyond the spec list:

- non-CSI-u escape sequence (F5 `\e[15~`) forwarded verbatim: `TestRunChordFSM_NonCSIuEscapeForwarded`
- mixed legacy + kitty stream still detaches: `TestRunChordFSM_LegacyAndKittyMixed`
- whole chord vocabulary works under kitty encoding (literal-prefix, suspend, help, clone): `TestRunChordFSM_OtherChordsCSIu`
- CSI u parameter parser corner cases (missing modifier, alternate-key form, event-tail on modifier): `TestParseCSIuParams`
- prefix matcher accepts shifted+base codepoints with ctrl, rejects no-ctrl: `TestIsPrefixKey`

### Full suite

`go test ./...`:

```
ok  	github.com/hayeah/hootty	0.527s
ok  	github.com/hayeah/hootty/cmd/hoot	3.224s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	1.072s
ok  	github.com/hayeah/hootty/internal/sshtransport	0.831s
```

### Manual smoke test (pending human verification)

This subagent runs in a non-interactive environment and cannot drive a real
Ghostty + Claude Code session. To verify on the human's box after merge:

1. Install the new binary from the main checkout:
   `gobin install ~/github.com/hayeah/hootty/cmd/hoot`
2. In Ghostty, start a `hoot` session and attach Claude Code inside it
   (or any TUI that emits `\x1b[>1u` to enable kitty mode).
3. Press `C-^` then `.` — the attached client should detach (return to
   the outer shell with the `[connected. …]` line gone) instead of the
   chord being forwarded into Claude Code as raw input.
4. Repeat under iTerm — should continue to work as before (legacy bytes).
5. Repeat under Ghostty + zsh (no inner TUI) — should continue to work
   as before.

If a quick byte-level check is wanted without Claude Code: from a Ghostty
shell with kitty mode enabled (e.g. via `printf '\x1b[>1u'`), `cat` and
press `C-^`. Ghostty will echo `\x1b[94;6u` (or similar); that exact
sequence is exercised by `TestRunChordFSM_KittyShifted`.

## Trouble report

- Spec said "`\e[94;6u <other>` → forward both verbatim, do not detach";
  the legacy FSM behavior for an unrecognized chord follow-up is silent
  ignore (consume both, forward neither). The new code preserves that
  legacy semantics rather than introducing an asymmetry between
  encodings — the test asserts only "no detach". Noted in
  `TestRunChordFSM_KittyPrefixUnknownFollowup` comment for the next
  reader.
- `go.mod` had pre-existing `should be direct` warnings on
  `github.com/coder/websocket` and `golang.org/x/term`; left untouched
  (not in scope for this section).
