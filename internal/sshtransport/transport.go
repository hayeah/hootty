package sshtransport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxUnixSocketPathLen = 80

type Config struct {
	User     string
	Host     string
	Port     string
	StateDir string
}

type Plan struct {
	Target        string
	ControlPath   string
	LocalSocket   string
	RemoteSocket  string
	RemoteBind    string
	ForwardTarget string
	RemotePID     string
}

type Tunnel struct {
	cmd         *exec.Cmd
	done        chan error
	localSocket string
	stderr      *bytes.Buffer
	cleanupArgs []string

	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, cfg Config) (*Tunnel, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("ssh remote: missing host")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir tunnel state dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(ControlPath(cfg.StateDir, cfg.User, cfg.Host, cfg.Port)), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir ssh control dir: %w", err)
	}

	home, err := remoteHome(ctx, cfg)
	if err != nil {
		return nil, err
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	plan := BuildPlan(cfg, id, home)
	if err := os.MkdirAll(filepath.Dir(plan.LocalSocket), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir local forward dir: %w", err)
	}
	if err := os.Remove(plan.LocalSocket); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale local socket: %w", err)
	}

	args := TunnelArgs(cfg, plan)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ssh tunnel: %w", err)
	}

	t := &Tunnel{
		cmd:         cmd,
		done:        make(chan error, 1),
		localSocket: plan.LocalSocket,
		stderr:      &stderr,
		cleanupArgs: CleanupArgs(cfg, plan),
	}
	go func() {
		t.done <- cmd.Wait()
	}()
	if err := waitHTTPReady(ctx, plan.LocalSocket, t.done); err != nil {
		_ = t.Close()
		if text := strings.TrimSpace(stderr.String()); text != "" {
			return nil, fmt.Errorf("%w: %s", err, text)
		}
		return nil, err
	}
	return t, nil
}

func (t *Tunnel) LocalSocket() string {
	return t.localSocket
}

func (t *Tunnel) Close() error {
	t.closeOnce.Do(func() {
		if t.cmd == nil || t.cmd.Process == nil {
			return
		}
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM)
		if len(t.cleanupArgs) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = exec.CommandContext(ctx, "ssh", t.cleanupArgs...).Run()
			cancel()
		}
		select {
		case <-t.done:
		case <-time.After(time.Second):
			_ = t.cmd.Process.Kill()
			select {
			case <-t.done:
			case <-time.After(time.Second):
				t.closeErr = fmt.Errorf("ssh tunnel did not exit after SIGTERM/SIGKILL")
			}
		}
		_ = os.Remove(t.localSocket)
	})
	return t.closeErr
}

func BuildPlan(cfg Config, id, remoteHome string) Plan {
	target := cfg.Host
	if cfg.User != "" {
		target = cfg.User + "@" + cfg.Host
	}
	safeHost := strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(target)
	if cfg.Port != "" {
		safeHost += "_" + cfg.Port
	}
	port := remotePort(id)
	return Plan{
		Target:        target,
		ControlPath:   ControlPath(cfg.StateDir, cfg.User, cfg.Host, cfg.Port),
		LocalSocket:   localSocketPath(cfg.StateDir, safeHost, id),
		RemoteSocket:  path.Join(remoteHome, ".hoot", ".tunnels", id+".sock"),
		RemoteBind:    fmt.Sprintf("127.0.0.1:%d", port),
		ForwardTarget: fmt.Sprintf("127.0.0.1:%d", port),
		RemotePID:     path.Join(remoteHome, ".hoot", ".tunnels", id+".pid"),
	}
}

func TunnelArgs(cfg Config, plan Plan) []string {
	args := baseArgs(cfg, plan.ControlPath)
	remoteCommand := "sh -lc " + shellQuote(remoteServeScript(plan.RemoteBind, plan.RemotePID))
	args = append(args,
		"-o", "ExitOnForwardFailure=yes",
		"-L", plan.LocalSocket+":"+plan.ForwardTarget,
		plan.Target,
		remoteCommand,
	)
	return args
}

func CleanupArgs(cfg Config, plan Plan) []string {
	args := baseArgs(cfg, plan.ControlPath)
	script := "if test -f " + shellQuote(plan.RemotePID) +
		"; then pid=$(cat " + shellQuote(plan.RemotePID) +
		"); kill \"$pid\" 2>/dev/null; wait \"$pid\" 2>/dev/null; rm -f " + shellQuote(plan.RemotePID) +
		"; fi"
	return append(args, plan.Target, "sh -lc "+shellQuote(script))
}

func remoteServeScript(remoteBind, remotePID string) string {
	pidDir := path.Dir(remotePID)
	return "mkdir -p " + shellQuote(pidDir) +
		"; hoot serve --bind " + shellQuote(remoteBind) +
		" & pid=$!; echo \"$pid\" > " + shellQuote(remotePID) +
		"; trap 'kill \"$pid\" 2>/dev/null; wait \"$pid\" 2>/dev/null; rm -f " + shellQuote(remotePID) +
		"; exit' HUP INT TERM EXIT; wait \"$pid\""
}

func ControlPath(stateDir, user, host, port string) string {
	sum := sha256.Sum256([]byte(controlKey(user, host, port)))
	name := "cm-" + hex.EncodeToString(sum[:4])
	candidate := filepath.Join(stateDir, name)
	if len(candidate) <= maxUnixSocketPathLen {
		return candidate
	}
	return filepath.Join(shortControlDir(), name)
}

func controlKey(user, host, port string) string {
	key := host
	if user != "" {
		key = user + "@" + key
	}
	if port != "" {
		key += ":" + port
	}
	return key
}

func shortControlDir() string {
	return shortSocketDir("ssh-control", "hootty-ssh-control-"+fmt.Sprint(os.Getuid()), "cm-00000000")
}

func localSocketPath(stateDir, safeHost, id string) string {
	name := "fwd-" + safeHost + "-" + fmt.Sprint(os.Getpid()) + "-" + id + ".sock"
	candidate := filepath.Join(stateDir, name)
	if len(candidate) <= maxUnixSocketPathLen {
		return candidate
	}
	sum := sha256.Sum256([]byte(name))
	shortName := "fwd-" + hex.EncodeToString(sum[:8]) + ".sock"
	return filepath.Join(shortSocketDir("ssh-forward", "hootty-ssh-forward-"+fmt.Sprint(os.Getuid()), shortName), shortName)
}

func shortSocketDir(cacheName, tmpName, sampleName string) string {
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		candidate := filepath.Join(dir, "hootty", cacheName)
		if len(filepath.Join(candidate, sampleName)) <= maxUnixSocketPathLen {
			return candidate
		}
	}
	return filepath.Join(os.TempDir(), tmpName)
}

func remoteHome(ctx context.Context, cfg Config) (string, error) {
	plan := BuildPlan(cfg, "probe", "")
	args := baseArgs(cfg, plan.ControlPath)
	args = append(args, plan.Target, "sh -lc 'printf %s \"$HOME\"'")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(stderr.String())
		if text != "" {
			return "", fmt.Errorf("resolve remote home: %w: %s", err, text)
		}
		return "", fmt.Errorf("resolve remote home: %w", err)
	}
	home := strings.TrimSpace(stdout.String())
	if home == "" {
		return "", fmt.Errorf("resolve remote home: empty HOME")
	}
	return home, nil
}

func baseArgs(cfg Config, controlPath string) []string {
	args := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + controlPath,
		"-o", "ControlPersist=60",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
	}
	if cfg.Port != "" {
		args = append(args, "-p", cfg.Port)
	}
	return args
}

func waitHTTPReady(ctx context.Context, sockPath string, done <-chan error) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if err != nil {
				return fmt.Errorf("ssh tunnel exited before ready: %w", err)
			}
			return fmt.Errorf("ssh tunnel exited before ready")
		case <-deadline.C:
			return fmt.Errorf("ssh tunnel did not become HTTP-ready")
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hoot/healthz", nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func remotePort(id string) int {
	if len(id) < 4 {
		return 22000
	}
	buf, err := hex.DecodeString(id[:4])
	if err != nil || len(buf) != 2 {
		return 22000
	}
	n := int(buf[0])<<8 | int(buf[1])
	return 20000 + n%30000
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
