# Cross-compile `hoot` to Linux

This recipe builds a Linux `amd64` `hoot` binary from macOS. The hard part is
not Go cross-compilation; it is the cgo dependency on Ghostty's
`libghostty-vt`.

The working route is:

- Build Linux `libghostty-vt` directly from the Ghostty Zig build.
- Point `go-libghostty` at that Linux pkg-config prefix.
- Use Zig as the cgo C compiler for the Go Linux target.

Ghostty's Nix flake does package `libghostty-vt`, including static archive and
pkg-config metadata, but it does not currently provide a simpler downstream
macOS-host Go cross-build wrapper. The Zig route is the minimal reproducible
path.

## Prereqs

- Go 1.26
- Zig 0.15.2
- `pkg-config`
- Local checkouts:
  - `~/github.com/hayeah/hootty`
  - `~/github.com/ghostty-org/ghostty`
  - `~/github.com/mitchellh/go-libghostty`

`hootty/go.work` currently replaces `github.com/mitchellh/go-libghostty` with
`~/github.com/mitchellh/go-libghostty`, so keep that checkout present.

## Build

```sh
HOOTTY="$HOME/github.com/hayeah/hootty"
GHOSTTY="$HOME/github.com/ghostty-org/ghostty"
PREFIX="$HOOTTY/.cross/linux-amd64/libghostty-vt"

mkdir -p "$PREFIX"

cd "$GHOSTTY"
zig build \
  -Dtarget=x86_64-linux-gnu \
  -Dcpu=baseline \
  -Doptimize=ReleaseFast \
  -Dapp-runtime=none \
  -Demit-lib-vt=true \
  --prefix "$PREFIX"

cd "$HOOTTY"
PKG_CONFIG_PATH="$PREFIX/share/pkgconfig" \
CGO_ENABLED=1 \
GOOS=linux \
GOARCH=amd64 \
CC="zig cc -target x86_64-linux-gnu" \
go build -o ./hoot-linux-amd64 ./cmd/hoot

file ./hoot-linux-amd64
```

Expected shape:

```text
./hoot-linux-amd64: ELF 64-bit LSB executable, x86-64, ... dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2, ... for GNU/Linux ...
```

The final binary is dynamically linked to standard glibc, but
`libghostty-vt` is linked from the static archive through
`go-libghostty`'s default cgo path. There is no separate `libghostty-vt` file
to install on the Linux host.

## Copy to a Linux host

```sh
ssh devbox 'mkdir -p ~/.local/bin'
rsync -av ./hoot-linux-amd64 devbox:~/.local/bin/hoot
ssh devbox 'chmod +x ~/.local/bin/hoot'
```

## Smoke test on `devbox`

```sh
ssh devbox '~/.local/bin/hoot --help'

ssh devbox \
  "~/.local/bin/hoot run --key linuxsmoke -- bash -c 'echo hello-from-linux; sleep 30'"

ssh devbox '~/.local/bin/hoot list'

ssh devbox '~/.local/bin/hoot serve --bind 127.0.0.1:21000 >/tmp/hoot-serve.log 2>&1 & echo $! >/tmp/hoot-serve.pid'
ssh devbox 'curl -sS localhost:21000/healthz'
```

For cross-machine attach, the server must listen on an address reachable from
the macOS host. A loopback-only `127.0.0.1` listener is enough for the health
check above, but not for `--host devbox:21000`.

```sh
ssh devbox 'kill "$(cat /tmp/hoot-serve.pid)"'
ssh devbox '~/.local/bin/hoot serve --bind 0.0.0.0:21000 >/tmp/hoot-serve.log 2>&1 & echo $! >/tmp/hoot-serve.pid'

# From macOS. C-^. detaches; this printf sends that prefix sequence after attach.
(sleep 2; printf '\036.') | ./bin/hoot attach \
  --host devbox:21000 \
  --no-reconnect \
  linuxsmoke
```
