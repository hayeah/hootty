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

type Config struct {
	User     string
	Host     string
	Port     string
	StateDir string
}

type Plan struct {
	Target       string
	ControlPath  string
	LocalSocket  string
	RemoteSocket string
}

type Tunnel struct {
	cmd         *exec.Cmd
	done        chan error
	localSocket string
	stderr      *bytes.Buffer

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

	home, err := remoteHome(ctx, cfg)
	if err != nil {
		return nil, err
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	plan := BuildPlan(cfg, id, home)
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
		select {
		case <-t.done:
		case <-time.After(time.Second):
			_ = t.cmd.Process.Kill()
			<-t.done
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
	return Plan{
		Target:       target,
		ControlPath:  ControlPath(cfg.StateDir, cfg.User, cfg.Host, cfg.Port),
		LocalSocket:  filepath.Join(cfg.StateDir, "fwd-"+safeHost+"-"+fmt.Sprint(os.Getpid())+"-"+id+".sock"),
		RemoteSocket: path.Join(remoteHome, ".hoot", ".tunnels", id+".sock"),
	}
}

func TunnelArgs(cfg Config, plan Plan) []string {
	args := baseArgs(cfg, plan.ControlPath)
	remoteCommand := "sh -lc " + shellQuote(remoteServeScript(plan.RemoteSocket))
	args = append(args,
		"-o", "ExitOnForwardFailure=yes",
		"-L", plan.LocalSocket+":"+plan.RemoteSocket,
		plan.Target,
		remoteCommand,
	)
	return args
}

func remoteServeScript(remoteSocket string) string {
	return "hoot serve --bind " + shellQuote("unix:"+remoteSocket) +
		" & pid=$!; trap 'kill \"$pid\" 2>/dev/null; wait \"$pid\" 2>/dev/null; exit' HUP INT TERM EXIT; wait \"$pid\""
}

func ControlPath(stateDir, user, host, port string) string {
	sum := sha256.Sum256([]byte(controlKey(user, host, port)))
	return filepath.Join(stateDir, "cm-"+hex.EncodeToString(sum[:4]))
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
