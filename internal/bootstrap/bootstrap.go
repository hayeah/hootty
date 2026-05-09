// Package bootstrap installs/upgrades the hoot binary on a remote SSH host.
//
// Three modes mirror the spec's install.sh integration points:
//
//   ModeAuto      pipe install.sh through ssh; remote downloads from GitHub.
//   ModeUpload    detect remote os/arch, download tarball locally, scp to
//                 remote, install via install.sh --from-file.
//   ModeFromFile  caller-supplied local tarball; scp + install (no download).
//
// All three trust the SSH channel for transport security and rely on either
// install.sh's SHA-256 verification (ModeAuto) or local-side verification
// (ModeUpload) for tarball integrity. ModeFromFile assumes the caller
// vouches for the tarball.
package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "embed"
)

//go:embed install.sh
var installScript []byte

// InstallScript returns the embedded installer for callers that want to
// invoke it via a different transport. Bytes are owned by the package; do
// not mutate.
func InstallScript() []byte {
	return installScript
}

type Mode int

const (
	ModeAuto Mode = iota
	ModeUpload
	ModeFromFile
)

func (m Mode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModeUpload:
		return "upload"
	case ModeFromFile:
		return "from-file"
	}
	return fmt.Sprintf("Mode(%d)", int(m))
}

// Options configures Install.
//
// Host is required. User and Port are optional. Version is required for
// ModeAuto and ModeUpload; Tarball is required for ModeFromFile.
type Options struct {
	User string
	Host string
	Port string

	Mode Mode

	// Version is the release tag to install (e.g. "v0.0.1").
	Version string
	// Tarball is a local path to a hoot-<os>-<arch>.tar.gz for ModeFromFile.
	Tarball string

	// InstallDir is the remote install directory. Empty defers to install.sh's
	// default ($HOOT_INSTALL_DIR or ~/.local/bin).
	InstallDir string

	Stdout io.Writer
	Stderr io.Writer
}

// Probe runs `hoot version` on the remote and returns what it printed.
//
// Returns ("", nil) when the binary is missing (no `hoot` on PATH or non-zero
// exit). The caller's outer ssh failures (auth, connectivity) come back as
// errors. The probe must NOT bring down a hoot @host flow: a clean
// "missing" answer is what tells the caller to install.
func Probe(ctx context.Context, user, host, port string) (string, error) {
	if host == "" {
		return "", errors.New("bootstrap.Probe: host is required")
	}
	args := sshArgs(user, host, port)
	args = append(args, "hoot version 2>/dev/null || echo __MISSING__")

	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(stderr.String())
		if text != "" {
			return "", fmt.Errorf("bootstrap.Probe: ssh: %w: %s", err, text)
		}
		return "", fmt.Errorf("bootstrap.Probe: ssh: %w", err)
	}
	line := strings.TrimSpace(stdout.String())
	if line == "" || line == "__MISSING__" {
		return "", nil
	}
	return line, nil
}

// Install ensures the hoot binary is present on the remote according to opts.
//
// Idempotent over Mode. It does NOT probe first — callers can do that with
// Probe and skip Install if the version already matches. (See `hoot @host`
// auto-bootstrap for the typical caller.)
func Install(ctx context.Context, opts Options) error {
	if opts.Host == "" {
		return errors.New("bootstrap.Install: Host is required")
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	switch opts.Mode {
	case ModeAuto:
		if opts.Version == "" {
			return errors.New("bootstrap.Install: Version required for ModeAuto")
		}
		return installAuto(ctx, opts)
	case ModeUpload:
		if opts.Version == "" {
			return errors.New("bootstrap.Install: Version required for ModeUpload")
		}
		return installUpload(ctx, opts)
	case ModeFromFile:
		if opts.Tarball == "" {
			return errors.New("bootstrap.Install: Tarball required for ModeFromFile")
		}
		return installFromFile(ctx, opts)
	default:
		return fmt.Errorf("bootstrap.Install: unknown mode %s", opts.Mode)
	}
}

// installAuto pipes install.sh through ssh; the remote downloads the tarball
// from GitHub itself. install.sh on the remote does the SHA-256 check.
func installAuto(ctx context.Context, opts Options) error {
	args := sshArgs(opts.User, opts.Host, opts.Port)
	cmdline := fmt.Sprintf("sh -s -- --version %s", shellQuote(opts.Version))
	if opts.InstallDir != "" {
		cmdline += fmt.Sprintf(" --install-dir %s", shellQuote(opts.InstallDir))
	}
	args = append(args, cmdline)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(installScript)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return cmd.Run()
}

// installUpload detects the remote platform, downloads the matching tarball
// locally (with checksum verification), scps it to the remote, then runs
// install.sh --from-file there.
func installUpload(ctx context.Context, opts Options) error {
	target, err := detectRemoteTarget(ctx, opts)
	if err != nil {
		return fmt.Errorf("detect remote platform: %w", err)
	}
	local, err := downloadTarball(ctx, opts.Version, target, opts.Stderr)
	if err != nil {
		return fmt.Errorf("download tarball: %w", err)
	}
	return uploadAndInstall(ctx, opts, local)
}

func installFromFile(ctx context.Context, opts Options) error {
	if _, err := os.Stat(opts.Tarball); err != nil {
		return fmt.Errorf("bootstrap.Install: tarball not found: %w", err)
	}
	return uploadAndInstall(ctx, opts, opts.Tarball)
}

// uploadAndInstall scps localTarball to a unique remote path, then runs
// install.sh --from-file remotely. The remote tarball is removed after.
func uploadAndInstall(ctx context.Context, opts Options, localTarball string) error {
	// /tmp/hoot-install-<pid>.tar.gz to avoid stomping concurrent installs.
	remotePath := fmt.Sprintf("/tmp/hoot-install-%d.tar.gz", os.Getpid())

	if err := scpUp(ctx, opts, localTarball, remotePath); err != nil {
		return fmt.Errorf("scp: %w", err)
	}

	args := sshArgs(opts.User, opts.Host, opts.Port)
	cmdline := fmt.Sprintf("sh -s -- --from-file %s", shellQuote(remotePath))
	if opts.InstallDir != "" {
		cmdline += fmt.Sprintf(" --install-dir %s", shellQuote(opts.InstallDir))
	}
	// Always clean up the staged tarball, even on install failure.
	cmdline = fmt.Sprintf("(%s); rc=$?; rm -f %s; exit $rc", cmdline, shellQuote(remotePath))
	args = append(args, cmdline)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(installScript)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return cmd.Run()
}

func scpUp(ctx context.Context, opts Options, local, remote string) error {
	args := []string{"-o", "BatchMode=yes"}
	if opts.Port != "" {
		args = append(args, "-P", opts.Port)
	}
	target := opts.Host
	if opts.User != "" {
		target = opts.User + "@" + opts.Host
	}
	args = append(args, local, target+":"+remote)
	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return cmd.Run()
}

func detectRemoteTarget(ctx context.Context, opts Options) (string, error) {
	args := sshArgs(opts.User, opts.Host, opts.Port)
	args = append(args, "uname -sm")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(stderr.String())
		if text != "" {
			return "", fmt.Errorf("ssh uname: %w: %s", err, text)
		}
		return "", fmt.Errorf("ssh uname: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(stdout.String()))
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected uname output: %q", stdout.String())
	}
	osPart, archPart := parts[0], parts[1]
	var os_, arch string
	switch osPart {
	case "Darwin":
		os_ = "darwin"
	case "Linux":
		os_ = "linux"
	default:
		return "", fmt.Errorf("unsupported remote OS %q", osPart)
	}
	switch archPart {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported remote arch %q", archPart)
	}
	if os_ == "darwin" && arch == "amd64" {
		return "", errors.New("remote is darwin-amd64; hoot does not currently ship that target")
	}
	return os_ + "-" + arch, nil
}

// downloadTarball fetches hoot-<target>.tar.gz + checksums.txt from GitHub
// releases for the given version, verifies the SHA-256 locally, and returns
// the on-disk path. Caches under $XDG_CACHE_HOME/hoot-bootstrap/<version>/
// so repeated installs against the same version reuse the file.
func downloadTarball(ctx context.Context, version, target string, stderr io.Writer) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheDir, "hoot-bootstrap", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	archive := fmt.Sprintf("hoot-%s.tar.gz", target)
	local := filepath.Join(dir, archive)
	checksums := filepath.Join(dir, "checksums.txt")

	if _, err := os.Stat(local); err == nil {
		if _, err := os.Stat(checksums); err == nil {
			if ok, verr := verifySHA(local, checksums, archive); verr == nil && ok {
				return local, nil
			}
			// fall through to re-download
			_ = os.Remove(local)
			_ = os.Remove(checksums)
		}
	}

	urlBase := fmt.Sprintf("https://github.com/hayeah/hootty/releases/download/%s", version)
	if err := curlDownload(ctx, urlBase+"/"+archive, local, stderr); err != nil {
		_ = os.Remove(local)
		return "", fmt.Errorf("download %s: %w", archive, err)
	}
	if err := curlDownload(ctx, urlBase+"/checksums.txt", checksums, stderr); err != nil {
		_ = os.Remove(local)
		_ = os.Remove(checksums)
		return "", fmt.Errorf("download checksums.txt: %w", err)
	}
	ok, err := verifySHA(local, checksums, archive)
	if err != nil {
		return "", err
	}
	if !ok {
		_ = os.Remove(local)
		_ = os.Remove(checksums)
		return "", fmt.Errorf("checksum mismatch for %s", archive)
	}
	return local, nil
}

func curlDownload(ctx context.Context, url, dest string, stderr io.Writer) error {
	fmt.Fprintf(stderr, "bootstrap: downloading %s\n", url)
	cmd := exec.CommandContext(ctx, "curl", "-fsSL", "-o", dest, url)
	cmd.Stderr = stderr
	return cmd.Run()
}

// verifySHA looks up the SHA for `archive` in the GNU shasum-format
// checksums.txt and compares it to the actual SHA-256 of `tarball`.
func verifySHA(tarball, checksumsPath, archive string) (bool, error) {
	body, err := os.ReadFile(checksumsPath)
	if err != nil {
		return false, err
	}
	var want string
	for _, line := range strings.Split(string(body), "\n") {
		// "<sha>  <filename>"
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[len(fields)-1] == archive {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return false, fmt.Errorf("no checksum entry for %s in %s", archive, checksumsPath)
	}
	f, err := os.Open(tarball)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	return got == want, nil
}

// sshArgs builds the leading argv for `ssh` — common flags, optional port,
// and the host/userhost target. The caller appends the remote command.
//
// BatchMode=yes makes ssh refuse password prompts; bootstrap is meant to
// flow over an already-trusted key/agent setup, and an interactive prompt
// here would silently hang a `hoot @host` flow.
func sshArgs(user, host, port string) []string {
	args := []string{"-o", "BatchMode=yes"}
	if port != "" {
		args = append(args, "-p", port)
	}
	target := host
	if user != "" {
		target = user + "@" + host
	}
	return append(args, target)
}

// shellQuote wraps s in single-quotes safe for POSIX sh, escaping any
// embedded single quote as `'\''`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
