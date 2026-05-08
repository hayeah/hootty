---
status: done
section: Make hoot ls human-friendly by default
slug: make-hoot-ls-human-friendly-by-default
mode: worktree
spec:
created: 2026-05-08T03:36:26Z
---

> ## Make hoot ls human-friendly by default
>
> ---
> status:
>   type: open
> ---
>
> `hoot ls` should print a human-readable status line by default — essentially the same line that's fed to the fzf matcher for session pickers — and hide exited sessions.
>
> - Default: pretty status lines, live sessions only.
> - `--json` — current JSONL output (the existing default behavior).
> - `--all` — include exited sessions.
>
> - [ ] implement and verify

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] add `--json` and `--all` flags to `cmdList` in `cmd/hoot/list.go`; default to pretty + alive-only
- [x] reuse `loadSessionList` (already returns `SessionWithMeta` with `Alive` for local + remote) instead of the bespoke local/remote split in `cmdList`
- [x] render pretty default via `sessionpick.Format` (same line shape fzf matcher sees)
- [x] update `list_test.go` — existing remote test moves to `--json`; add tests for default (pretty, alive only), `--all`, and the local pretty path
- [x] update README `hoot list` docs (synopsis line + the JSONL paragraph)
- [x] `go test ./...` passes; run `hoot ls` against a real local session to eyeball

## Agent log
- 2026-05-08T03:41Z list.go now reuses loadSessionList; default emits sessionpick.Format alive-only; --all includes dead; --json keeps JSONL (also alive-only by default, --all to include dead) — bc6ad7a + c2b411a

## Boss log

## Evidence

### Tests

```
$ cd repos/github.com/hayeah/hootty && go test ./...
ok  	github.com/hayeah/hootty	7.422s
ok  	github.com/hayeah/hootty/cmd/hoot	6.834s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/sessionpick	0.946s
ok  	github.com/hayeah/hootty/internal/shortid	(cached)
ok  	github.com/hayeah/hootty/internal/sshtransport	0.382s
```

Four new tests in `cmd/hoot/list_test.go` exercise each (pretty/json) × (alive-only/--all) cell against a `httptest` server with one alive + one dead session. They assert (a) line counts, (b) the `[alive1]` / `[dead01]` prefixes, (c) the `(dead)` tag only appears for dead sessions, (d) `--json` does not leak the serve-only `"alive"` JSON field.

### CLI transcript

Real local-state-dir run against an actual `hoot run` session, captured at `tmp/034032_241-cli-transcript.txt`. Highlights:

```
$ hoot list                    # default: pretty, live only
[demo01]	@local	~/Dropbox/boss/tasks/make-hoot-ls-human-friendly-by-default	bash -lc sleep 30

$ hoot list --all              # adds dead with (dead) tag
[reuse112157]	@local	~	bash -lc for i in $(seq 1 60); do echo reuse-local-$i; sleep 2; done	(dead)
[098]	@local	~/github.com/hayeah/agentboss/dashboard	zsh -l	(dead)
[r50]	@local	~	zsh -l	(dead)
[demo01]	@local	~/Dropbox/boss/tasks/make-hoot-ls-human-friendly-by-default	bash -lc sleep 30

$ hoot list --json             # JSONL, alive only
{"session":{"key":"demo01",...},"state":{...}}

$ hoot list --json --all | wc -l
       4
```

When there are no live sessions, default `hoot list` prints nothing — exactly what we want for the picker-shape default.

### Commits

- `bc6ad7a` Make hoot ls human-friendly by default (`cmd/hoot/list.go`, `cmd/hoot/list_test.go`)
- `c2b411a` README: document hoot ls --json / --all flags

## Trouble report

- The first run of `TestCmdListRemoteJSON` failed because the leak-check `strings.Contains(out, "alive")` matched the test fixture key `"alive1"`. Tightened to `"\"alive\""` to only match the JSON field name. (Pre-existing test had used `"abc123"` so the substring collision wasn't possible.)
- Decision: I made `--all` an orthogonal filter that applies to both formats (so `hoot list --json` defaults to alive-only too). The other reading — "`--json` reproduces the prior behavior verbatim, including dead sessions" — was tempting but would leave the two output modes with mismatched defaults, which is more confusing than the breaking change for scripts. Existing scripts that want every session can opt in with `--json --all`.
