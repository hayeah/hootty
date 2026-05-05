---
status: done
section: Rebrand supervise/supervisor → hootty/hoot
slug: rebrand-supervise-supervisor-hootty-hoot
mode: worktree
spec: spec.md
created: 2026-05-05T13:43:08Z
---

> ## Rebrand supervise/supervisor → hootty/hoot
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/supervisor` (repo stays on disk where it is for this PR; the GitHub rename + on-disk move is a separate operation).
>
> Mechanical rename across ~254 files, ~800 Go occurrences, plus TS workspace packages. Scope:
>
> - **Go module path**: `github.com/hayeah/supervisor` → `github.com/hayeah/hootty`. Update `go.mod`, all imports across the repo, the sibling `dotfiles` `replace` directive (`~/github.com/hayeah/dotfiles/libs/hayeah-go/...`) if any references our module, and any `go.work` files.
> - **CLI binary**: `supervise` → `hoot`. The internal `__supervise` subcommand → either `__hoot` or `hoot internal` (your call — pick whichever is cleaner; mention rationale in agent log).
> - **TS packages**: `@hayeah/supervisor-termui` → `@hayeah/hootty-termui`; `@hayeah/supervisor-webui` → `@hayeah/hootty-webui`. Update workspace package.json names + every consumer import. Verify `pnpm --filter ... build` still works.
> - **State dir default**: `~/.supervise` → `~/.hoot`. Update `defaultStateDir()` + any test fixtures that hardcode the path.
> - **README, help text, error messages**: anywhere the name appears in user-facing strings. Identifiers in code (var names, types, function names) too — full rename, no half-measures.
> - **Domain mention**: README can reference `hootty.dev` (landing page comes later — just put it in the README's tagline / footer).
> - **agentboss config keys** anywhere they reference "supervise" — verify none do, but check.
>
> The `gh repo rename` and the on-disk directory move are out of scope; the human handles those after lgtm. Just make sure the go.mod path matches the eventual GitHub URL so a post-rename `go install` works.
>
> Verification:
>
> - `go build ./...` clean
> - `go test ./...` green (every existing test should still pass — this is a rename, not a behavior change)
> - `pnpm -r build` (or per-package) clean for both TS packages
> - A built `hoot` binary runs: `hoot list`, `hoot run`, `hoot serve --bind ...`, `hoot attach --host ...` all behave as before
> - `~/.hoot` is created on first run (not `~/.supervise`)
> - README scan: zero remaining "supervise" / "supervisor" occurrences except where intentionally referencing the historical name
>
> Use codex.
>
> - [ ] do the rename, verify everything builds and tests, capture evidence

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->
- [x] Check out `hayeah/supervisor` and `hayeah/dotfiles`; inspect worklog/spec state
- [x] Write `spec.md` and seed worklog todos
- [x] Rename Go module/imports, CLI directory/binary/help, internal subcommand, state dir default, and Go identifiers/comments
- [x] Rename TS package directories/names, workspace consumers, scripts, and lockfile references
- [x] Update README/config/user-facing docs for `hoot`, `hootty`, `~/.hoot`, packages, and `hootty.dev`
- [x] Scan remaining `supervise`/`supervisor` occurrences and classify/fix
- [x] Run formatting and commit the mechanical rename
- [x] Verify Go build/test, TS builds, CLI smokes, default state-dir creation, dotfiles check, and README scan
- [x] Populate evidence and set status done

## Agent log
- 2026-05-05T13:44Z Checked out `github.com/hayeah/supervisor` and `github.com/hayeah/dotfiles`; dotfiles has no Go module/workspace reference to the supervisor module. Wrote `spec.md`; chose `__hoot` over `hoot internal` because it is the smallest faithful rename of the hidden dispatch.
- 2026-05-05T13:49Z Landed the full rebrand in `github.com/hayeah/supervisor` commit `a9cbc00`: Go module/package/API/state JSON moved to `github.com/hayeah/hootty`/`hootty`, CLI moved to `cmd/hoot` + `bin/hoot` + `__hoot`, TS packages moved to `packages/hootty-*`, README/devport/docs updated.
- 2026-05-05T13:58Z Follow-up from human naming feedback landed in commit `591cb7a`: hidden command is `hoot __session`; root package/API/state namespace/logging are `session` (`SessionConfig`, `StateFile.Session json:"session"`, `session.log`) while external module/CLI/TS brand remains `hootty`/`hoot`.

## Boss log

## Evidence

Repo commits on `github.com/hayeah/supervisor` branch `rebrand-supervise-supervisor-hootty-hoot`:

- `a9cbc00` — `Rebrand supervisor to hootty`
- `591cb7a` — `Rename internal runtime to session`

Post-commit verification:

```sh
$ go build ./...
# clean

$ go test ./...
ok  	github.com/hayeah/hootty	(cached)
ok  	github.com/hayeah/hootty/cmd/hoot	(cached)
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	(cached)

$ pnpm -r build
# @hayeah/hootty-termui tsup build clean
# @hayeah/hootty-webui tsc -b && vp build clean
```

CLI smoke transcript from `tmp/hoot-smoke.sh`:

```sh
== hoot list on empty explicit state ==
== default HOME creates .hoot, not .supervise ==
hoot: session "default" started (state-dir=.../tmp/hoot-smoke/home/.hoot)
.../tmp/hoot-smoke/home/.hoot/default
== hoot run explicit state ==
hoot: session "smoke" started (state-dir=.../tmp/hoot-smoke/state)
== hoot list after run ==
{"session":{"key":"smoke",...},"state":{"state":"running","cmd":"bash -lc echo hello-from-hoot; sleep 20",...}}
== hoot serve health/sessions ==
{"ok":true,"state_dir":".../tmp/hoot-smoke/state"}
{"sessions":[{"session":{"key":"smoke",...},"state":{"state":"running",...},"alive":true}]}
== hoot attach --host detach smoke ==
[connected. smoke @ 127.0.0.1:21357]
hello-from-hoothello-from-hoot
[disconnected. smoke @ 127.0.0.1:21357]
== cleanup remote smoke session ==
DELETE /sessions/smoke -> 204
```

Scans:

```sh
$ rg -n "supervise|supervisor|Supervise|Supervisor|\\.supervise|@hayeah/supervisor|github.com/hayeah/supervisor|__hoot|Hootty|json:\\\"hootty\\\"|hootty\\.log|session\\.dev|tmux__session" README.md packages package.json pnpm-lock.yaml devport.toml cmd internal *.go || true
# no matches

$ rg --files | rg 'supervise|supervisor|hootty\\.go|hootty_iface|hootty_test|cmd/hoot/hoot.go' || true
# no matches

$ cd repos/github.com/hayeah/dotfiles
$ rg -n "github.com/hayeah/supervisor|hayeah-go/supervisor|supervise|supervisor" -g 'go.mod' -g 'go.work' -g '!node_modules' || true
# no matches
```

Worktree status after commit:

```sh
$ git status --short --branch
## rebrand-supervise-supervisor-hootty-hoot
```

## Trouble report
- Dotfiles checkout has pre-existing untracked setup artifacts (`libs/hayeah-go/supervisor/cmd/ptydemo/.devport/`, `devport.env`, `skills/browser/node_modules`) from checkout/setup. Leaving them untouched unless they become relevant.
- `pnpm install` initially reported `core.hooksPath` still pointed at the old `packages/supervisor-webui/.vite-hooks/_`; I updated the worktree git config to `packages/hootty-webui/.vite-hooks/_`. This is local repo config, not a tracked file.
- `pnpm -r build` still emits the existing Vite chunk-size warning for `ghostty-web`; the build exits 0.
- Naming correction after initial completion: first pass used `__hoot` and `Hootty*` internals. Human clarified that the external brand should stay cute, but runtime/data names should be `session`; fixed in `591cb7a`.
