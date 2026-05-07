---
status: done
section: Hoot --remote default scheme — ssh
slug: hoot-remote-default-scheme-ssh
mode: worktree
spec:
created: 2026-05-07T04:01:32Z
---

> ## Hoot --remote default scheme — ssh
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> For the `--remote` flag (used by `hoot run --remote …`, `hoot attach --remote …`, `hoot list --remote …`, `hoot kill --remote …`, `hoot detach --remote …`), if the destination has no URL scheme, assume `ssh://`. Goal: `hoot run --remote m4mini -- bash -l` should Just Work without forcing the user to type `ssh://m4mini`.
>
> Walk through every `--remote` consumer (search for `--remote` flag and its plumbing through `httpClient`, `sshtransport`, etc.) and centralize the defaulting in one helper. Keep explicit schemes (`http://…`, `https://…`, `ssh://…`) honored as-is.
>
> - [ ] implement and verify
>   - centralize remote-URL parsing into a helper that defaults bare `host` (and `host:port`, `user@host`, `user@host:port`) to `ssh://`
>   - apply it at every `--remote` entry point
>   - tests: bare host, `user@host`, `host:port`, explicit `ssh://`, explicit `http(s)://` (still works)
>   - evidence: `go test ./...` passing tail + a `hoot list --remote <localhost-via-ssh>` smoke transcript proving an unprefixed host resolves to ssh

## Todos
<!-- Finer-grained than the boss-doc top-level checkboxes. Tick off as you go. -->

- [x] flip `parseRemoteFlag` default scheme from `http://` to `ssh://`
- [x] update `remote_test.go` cases (bare host, user@host, user@host:port; existing bare-hostport flips to ssh)
- [x] fix tests that pass `Listener.Addr().String()` to `--remote` (clone_test, list_test, run_attach_test, remote_test) — now need explicit `http://`
- [x] update --remote help/doc strings (main.go, attach.go, detach.go, kill.go, run.go, clone.go, list.go) + README
- [x] run `go test ./...`
- [x] smoke `hoot list --remote localhost` over ssh, capture transcript

## Agent log
- 2026-05-07T04:05Z implement+verify landed: parseRemoteFlag default flipped to ssh:// (bare host, host:port, user@host, user@host:port). go test ./... passing. smoke: hoot list --remote localhost dials over ssh (4b0440f)

## Boss log

## Evidence

### `go test ./...` (full suite, hootty repo)

```
ok  	github.com/hayeah/hootty	6.879s
ok  	github.com/hayeah/hootty/cmd/hoot	2.917s
?   	github.com/hayeah/hootty/internal/attachetest	[no test files]
?   	github.com/hayeah/hootty/internal/attachwire	[no test files]
ok  	github.com/hayeah/hootty/internal/shortid	1.254s
ok  	github.com/hayeah/hootty/internal/sshtransport	1.022s
```

`TestParseRemoteFlag` covers: empty, **bare host (m4mini)**,
**bare hostport (m4mini:20000 → ssh)**, **bare user@host (me@m4mini)**,
**bare user@host:port (me@m4mini:2222)**, http, https,
ssh://devbox, ssh://me@devbox:2222, unsupported scheme, missing host.
`TestSSHRemoteLocalhostHealthz` exercises the ssh transport end-to-end
when passwordless ssh localhost is available.

### Smoke: `hoot list --remote localhost` resolves to ssh

Full transcript at `tmp/smoke-list-remote-localhost.txt`. Highlights:

```
$ hoot list --remote localhost --state-dir <tmp>
{"session":{"key":"reuse112157", ... }}
[exit=0]

$ ls $SD/.tunnels   # confirms ssh tunnel was opened
cm-49960de5

$ hoot list --remote ssh://localhost --state-dir <tmp>   # explicit ssh still works
{"session":{"key":"reuse112157", ... }}
[exit=0]

$ hoot list --remote bogus://localhost --state-dir <tmp>
hoot list: --remote: unsupported scheme "bogus"
[exit=1]
```

The bare `localhost` form created an ssh tunnel state dir
(`$SD/.tunnels/cm-…`), proving the bare-host path went through
`newSSHRemote` and not `newHTTPRemote`. Output is identical to the
explicit `ssh://localhost` invocation.

## Trouble report

Nothing surprising. `parseRemoteFlag` was already centralized as the
single entry point used by `run`, `attach`, `list`, `kill`, `detach`,
`clone`, and `resolve`, so the implementation reduced to a one-line
default change plus test/doc updates. The only fanout was four test
files that had been passing `httptest.Server.Listener.Addr().String()`
(bare host:port) as `--remote` — those needed an explicit `http://`
prefix now that bare host:port routes to ssh.
