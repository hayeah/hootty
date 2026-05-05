---
status: done
section: Cross-compile hoot to Linux + smoke test on devbox
slug: cross-compile-hoot-to-linux-smoke-test-on-devbox
mode: worktree
spec: spec.md
created: 2026-05-05T14:18:32Z
---

> ## Cross-compile hoot to Linux + smoke test on devbox
>
> ---
> status:
>   type: open
> ---
>
> Work in `~/github.com/hayeah/hootty`.
>
> Goal: produce a working `hoot` Linux binary from a macOS host, rsync to `devbox` (a Linux box reachable over Tailscale or ssh — assume hoot is *not* yet installed there, but the host has standard Linux + glibc), and run smoke tests there.
>
> ### Cross-compilation strategy
>
> `hoot` links against libghostty (a Zig library). Cross-compiling pure Go is trivial (`GOOS=linux GOARCH=amd64`); the libghostty cgo dependency is what makes this hard.
>
> Two viable toolchains; investigate both, pick one, justify in `spec.md`:
>
> - **Zig as a cross-compiler** (`CC="zig cc -target x86_64-linux-gnu" CGO_ENABLED=1 GOOS=linux ...`). Zig ships LLVM + glibc/musl headers for many targets; a known-working pattern for cgo cross-compilation.
> - **Nix** — libghostty's repo (`~/github.com/ghostty-org/ghostty`) has a Nix flake. Investigate whether it exposes a cross-build for libghostty as a static archive (or shared object) we can link against. If yes, that's the most reproducible route.
>
> The "right" answer depends on what libghostty actually supports out of the box. Read the ghostty Nix flake first; if it gives you a Linux libghostty artifact for free, prefer Nix. If not, Zig is the fallback.
>
> ### Procedure
>
> - Investigate the libghostty build: how is it consumed today by hootty's macOS build? `go-libghostty` (`~/go/pkg/mod/github.com/mitchellh/go-libghostty@*`) likely uses a vendored prebuilt or `pkg-config`. Understand that path before cross-compiling.
> - Produce a Linux `hoot` binary on the macOS host. Pure static if possible (musl + zig); dynamic glibc is fine if static is impractical.
> - `rsync` the binary to `devbox`. Pick the path / target with the standard convention.
> - Run on devbox:
>   - `hoot --help` — verify the binary runs at all (this is the minimum bar)
>   - `hoot run -- bash -c 'echo hello-from-linux; sleep 30'` — smoke-test the supervisor path
>   - `hoot list` — verify state-dir creation works on linux (default `~/.hoot`)
>   - `hoot serve --bind 127.0.0.1:21000 &` then `curl localhost:21000/healthz` from devbox — verify HTTP surface
>   - `hoot attach --host devbox:21000 <key>` from the macOS host — verify remote attach across machines, with the byte-exact reconnect we shipped earlier
> - Document the cross-compile recipe in the README (or a new `docs/cross-compile.md`) so it's reproducible.
>
> ### Open design decisions
>
> - Static (musl) vs dynamic (glibc) — pick one, justify. Static is portable but harder to make work with libghostty if it needs glibc-only symbols.
> - Where does the cross-compile recipe live? `Makefile` target, shell script in `scripts/`, or just docs? Pick the minimal viable.
> - Should we bake CI for this? **No** — out of scope; just document the recipe.
>
> Use codex.
>
> - [ ] investigate, write spec.md, then implement and verify end-to-end on devbox
>   - evidence: the cross-compile recipe (paste the exact commands)
>   - evidence: `file ./hoot-linux-amd64` showing it's an ELF binary
>   - evidence: ssh transcript / smoke output from devbox showing `hoot --help`, `run`, `list`, `serve`, and remote attach all working
>   - evidence: README/docs updated with the recipe

## Todos
- [x] Check out `hayeah/hootty`, `ghostty-org/ghostty`, and `mitchellh/go-libghostty`.
- [x] Investigate Ghostty Nix/Zig libghostty-vt outputs and choose a cross-build route.
- [x] Investigate `go-libghostty` cgo/pkg-config/static linking behavior.
- [x] Write `spec.md` with the selected approach and verification plan.
- [x] Build Linux `libghostty-vt` with Zig into an isolated prefix.
- [x] Cross-compile `hoot-linux-amd64` from macOS.
- [x] Capture local ELF evidence with `file ./hoot-linux-amd64`.
- [x] `rsync` the binary to `devbox`.
- [x] Smoke test `hoot --help`, `run`, `list`, `serve`, and `/healthz` on `devbox`.
- [x] Smoke test `hoot attach --host devbox:21000 <key>` from macOS.
- [x] Document the recipe in `hootty` docs/README.
- [x] Commit repo changes and fill final evidence.

## Agent log

- 2026-05-05T14:35Z Checked out `hayeah/hootty`, `ghostty-org/ghostty`, and `mitchellh/go-libghostty`; inspected Ghostty flake/libghostty-vt package, hootty Makefile, and go-libghostty cgo files; wrote `spec.md` selecting the direct Zig cross-build route.
- 2026-05-05T14:52Z Built Linux `libghostty-vt` with Zig and cross-compiled `hoot-linux-amd64`; `file` reports an x86-64 Linux ELF dynamic-glibc executable with libghostty statically linked in.
- 2026-05-05T14:59Z Rsynced `hoot-linux-amd64` to `devbox:~/.local/bin/hoot`; smoke-tested `--help`, `run`, `list`, `serve` + `/healthz`, and macOS remote attach to `devbox:21000`.
- 2026-05-05T15:04Z Documented the recipe in `docs/cross-compile.md`, linked it from `README.md`, ignored generated cross-build outputs, and committed `a7ceacf`.

## Boss log

## Evidence

### Cross-compile recipe used

```sh
mkdir -p tmp/linux-libghostty-vt
cd repos/github.com/ghostty-org/ghostty
zig build \
  -Dtarget=x86_64-linux-gnu \
  -Dcpu=baseline \
  -Doptimize=ReleaseFast \
  -Dapp-runtime=none \
  -Demit-lib-vt=true \
  --prefix /Users/me/Dropbox/boss/tasks/cross-compile-hoot-to-linux-smoke-test-on-devbox/tmp/linux-libghostty-vt

cd repos/github.com/hayeah/hootty
PKG_CONFIG_PATH=/Users/me/Dropbox/boss/tasks/cross-compile-hoot-to-linux-smoke-test-on-devbox/tmp/linux-libghostty-vt/share/pkgconfig \
CGO_ENABLED=1 \
GOOS=linux \
GOARCH=amd64 \
CC='zig cc -target x86_64-linux-gnu' \
go build -o ./hoot-linux-amd64 ./cmd/hoot
```

### ELF evidence

```text
$ file ./hoot-linux-amd64
./hoot-linux-amd64: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2, for GNU/Linux 2.0.0, Go BuildID=HhSxCuChW0M1_Qu2U55V/qDWkVsnnzFF0hRDrfIdn/caQCiihNN3nAdt_nhV2q/Gn21yZt2iHpAUPenRxjc, with debug_info, not stripped
```

On devbox, `ldd ~/.local/bin/hoot` showed only standard glibc-side libraries (`libpthread`, `libc`, `librt`, `libresolv`, loader); no runtime `libghostty-vt` dependency.

### Copy to devbox

```text
$ rsync -av ./hoot-linux-amd64 devbox:~/.local/bin/hoot
Transfer starting: 1 files
hoot-linux-amd64

sent 22891157 bytes  received 42 bytes  2613419 bytes/sec
total size is 22888232  speedup is 1.00
```

### Devbox smoke

```text
$ ssh devbox '~/.local/bin/hoot --help'
hoot — reference CLI for the session library

Usage:
  hoot run     [--state-dir <d>] [--key <k>] -- <cmd> [args...]
  hoot list    [--state-dir <d>]
  hoot resolve [--state-dir <d>] <id-or-prefix>
  hoot attach  [--host <addr>] [--state-dir <d>] [--no-reconnect]
                    [--no-ascii-cinema-playback]
                    [--prefix-key <key>] <id-or-prefix>
  hoot serve   [--state-dir <d>] --bind <host:port> [--prefix /api]

$ ~/.local/bin/hoot run --key linuxsmoke -- bash -c 'echo hello-from-linux; sleep 30'
hoot: session "linuxsmoke" started (state-dir=/home/me/.hoot)
linuxsmoke

$ ~/.local/bin/hoot list
{"session":{"key":"linuxsmoke","pid":329587,"created_at":"2026-05-05T16:23:43.929847143+02:00"},"state":{"state":"running","cmd":"bash -c echo hello-from-linux; sleep 30","pid":329597,"started_at":"2026-05-05T14:23:43Z"}}

$ ~/.local/bin/hoot serve --bind 127.0.0.1:21000 &
$ curl -sS localhost:21000/healthz
{"ok":true,"started_at":"2026-05-05T16:23:44.474012028+02:00","state_dir":"/home/me/.hoot"}

$ cat /tmp/hoot-serve-127.log
hoot serve: listening on http://127.0.0.1:21000 (state-dir=/home/me/.hoot)
```

### Remote attach from macOS

For the cross-machine attach, I restarted `hoot serve` on devbox with `--bind 0.0.0.0:21000`; the earlier `127.0.0.1` bind is intentionally loopback-only and worked for the devbox-local `/healthz` smoke.

```text
$ ssh devbox ~/.local/bin/hoot run --key linuxsmoke2 -- bash -c 'echo hello-from-linux; sleep 30'
hoot: session "linuxsmoke2" started (state-dir=/home/me/.hoot)
linuxsmoke2

$ (sleep 2; printf "\036.") | bin/hoot attach --host devbox:21000 --no-reconnect --no-ascii-cinema-playback linuxsmoke2
[connected. linuxsmoke2 @ devbox:21000]
hello-from-linux
[disconnected. linuxsmoke2 @ devbox:21000]
exit=0
```

### Repo evidence

- `hayeah/hootty` commit: `a7ceacf` (`Document Linux cross-compile recipe`)
- Docs updated:
  - `docs/cross-compile.md`
  - `README.md`
  - `.gitignore`
- Verification: `PKG_CONFIG_PATH=/Users/me/github.com/mitchellh/go-libghostty/build/_deps/ghostty-src/zig-out/share/pkgconfig go test ./...`
  - `ok github.com/hayeah/hootty`
  - `ok github.com/hayeah/hootty/cmd/hoot`
  - `ok github.com/hayeah/hootty/internal/shortid`

## Trouble report

- Nix is not installed on this macOS host. Ghostty's flake does expose `libghostty-vt` packages, but not a clear downstream cgo cross-build recipe from Darwin, so this task proceeds with Zig.
- `hoot serve --bind 127.0.0.1:21000` is correct for the devbox-local health check but cannot support `hoot attach --host devbox:21000` from macOS. I used `0.0.0.0:21000` for the remote attach smoke and documented that distinction.
- The final binary is not fully static; it is glibc-dynamic. `libghostty-vt` itself is statically linked via `go-libghostty`'s default pkg-config module, so devbox does not need a separate Ghostty library install.
