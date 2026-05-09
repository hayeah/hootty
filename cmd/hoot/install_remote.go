package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/hayeah/hootty/internal/bootstrap"
)

func cmdInstallRemote(args []string) error {
	fs := flag.NewFlagSet("install-remote", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: hoot install-remote [flags] <host>

Install or upgrade hoot on a remote SSH host. By default the remote downloads
the matching tarball from GitHub itself; use --upload to download locally and
scp it up, or --from-file to ship a tarball you already have on disk.

  --version vX.Y.Z   release tag to install (default: this binary's version)
  --upload           fetch tarball locally with checksum verification, then
                     scp + install (use when the remote can't reach GitHub)
  --from-file PATH   skip download; scp this local tarball and install
  --install-dir DIR  remote install dir (default: ~/.local/bin on the remote)

Host accepts: host, user@host, host:port, or user@host:port. ssh agent / key
authentication is required — interactive password prompts are disabled.
`)
	}
	versionFlag := fs.String("version", "", "release tag to install (default: this binary's version)")
	upload := fs.Bool("upload", false, "fetch tarball locally and scp it (vs. remote-side download)")
	fromFile := fs.String("from-file", "", "local tarball path; skips download entirely")
	installDir := fs.String("install-dir", "", "remote install directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New("install-remote: expected exactly one <host> argument (got " + fmt.Sprintf("%d", len(rest)) + ")")
	}

	user, host, port, err := parseSSHTarget(rest[0])
	if err != nil {
		return err
	}

	opts := bootstrap.Options{
		User:       user,
		Host:       host,
		Port:       port,
		InstallDir: *installDir,
		Version:    *versionFlag,
		Tarball:    *fromFile,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}

	// Mode selection: --from-file wins, then --upload, else default Auto.
	switch {
	case *fromFile != "":
		if *upload {
			return errors.New("install-remote: --from-file and --upload are mutually exclusive")
		}
		opts.Mode = bootstrap.ModeFromFile
	case *upload:
		opts.Mode = bootstrap.ModeUpload
	default:
		opts.Mode = bootstrap.ModeAuto
	}

	// Default version: pin to this CLI's compiled-in tag, so the remote ends
	// up matching the local CLI exactly. Zed does the same — keeps the protocol
	// from drifting between client and helper. ModeFromFile doesn't need a
	// Version (the user-supplied tarball *is* the version).
	if opts.Version == "" && opts.Mode != bootstrap.ModeFromFile {
		if Version == "dev" {
			return errors.New("install-remote: this is a dev build with no embedded version; pass --version explicitly")
		}
		opts.Version = Version
	}

	return bootstrap.Install(context.Background(), opts)
}

// parseSSHTarget splits raw forms (host, user@host, host:port, user@host:port,
// or an explicit ssh://...) into (user, host, port).
//
// Lifted from the same shape as parseRemoteFlag's ssh:// branch in remote.go,
// but without the tunnel-state-dir setup — install-remote is a one-shot
// command that doesn't reuse hoot's persistent ssh control-master.
func parseSSHTarget(raw string) (user, host, port string, err error) {
	if !strings.Contains(raw, "://") {
		raw = "ssh://" + raw
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		return "", "", "", fmt.Errorf("invalid host %q: %w", raw, perr)
	}
	if u.Scheme != "ssh" {
		return "", "", "", fmt.Errorf("unsupported scheme %q (only ssh:// is accepted)", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", "", "", fmt.Errorf("missing host in %q", raw)
	}
	if u.User != nil {
		user = u.User.Username()
	}
	return user, u.Hostname(), u.Port(), nil
}
