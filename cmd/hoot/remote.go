package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hayeah/hootty/internal/bootstrap"
	"github.com/hayeah/hootty/internal/sshtransport"
)

type remoteKind string

const (
	remoteKindHTTP remoteKind = "http"
	remoteKindSSH  remoteKind = "ssh"
)

type Remote struct {
	kind    remoteKind
	display string

	Dial  func(ctx context.Context) (net.Conn, error)
	Close func() error

	httpBase *url.URL
	ssh      sshRemoteConfig
}

type sshRemoteConfig struct {
	User     string
	Host     string
	Port     string
	StateDir string
}

func parseRemoteFlag(raw, stateDir string) (*Remote, error) {
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "ssh://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("--remote: %w", err)
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""

	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return nil, fmt.Errorf("--remote: missing host")
		}
		return newHTTPRemote(u), nil
	case "ssh":
		if u.Host == "" {
			return nil, fmt.Errorf("--remote: missing host")
		}
		return newSSHRemote(u, stateDir), nil
	default:
		return nil, fmt.Errorf("--remote: unsupported scheme %q", u.Scheme)
	}
}

func newHTTPRemote(u *url.URL) *Remote {
	base := *u
	return &Remote{
		kind:     remoteKindHTTP,
		display:  base.String(),
		httpBase: &base,
		Dial: func(ctx context.Context) (net.Conn, error) {
			switch base.Scheme {
			case "http":
				var d net.Dialer
				return d.DialContext(ctx, "tcp", base.Host)
			case "https":
				d := &tls.Dialer{Config: &tls.Config{ServerName: hostFromHostport(base.Host)}}
				return d.DialContext(ctx, "tcp", base.Host)
			default:
				return nil, fmt.Errorf("unsupported scheme %q", base.Scheme)
			}
		},
		Close: func() error { return nil },
	}
}

func newSSHRemote(u *url.URL, stateDir string) *Remote {
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	cfg := sshRemoteConfig{
		User:     user,
		Host:     u.Hostname(),
		Port:     u.Port(),
		StateDir: filepath.Join(stateDir, ".tunnels"),
	}
	display := "ssh://"
	if user != "" {
		display += user + "@"
	}
	display += cfg.Host
	if cfg.Port != "" {
		display += ":" + cfg.Port
	}
	var mu sync.Mutex
	var tunnel *sshtransport.Tunnel
	var bootstrapped bool
	openTunnel := func(ctx context.Context) (*sshtransport.Tunnel, error) {
		// Ensure the remote has a matching `hoot` binary on PATH before we try
		// to bring up the tunnel — sshtransport.Open invokes `hoot serve` on
		// the remote, so a missing/wrong-version binary would otherwise
		// surface as an opaque "remote socket not ready" timeout.
		//
		// Probe-once: only the very first openTunnel invocation runs the
		// install path. A reopen after a socket error means hoot was already
		// running and we just got a transient drop; re-installing would be
		// wasteful and would briefly disrupt the running session.
		if !bootstrapped {
			if err := ensureRemoteHoot(ctx, cfg); err != nil {
				return nil, err
			}
			bootstrapped = true
		}
		return sshtransport.Open(ctx, sshtransport.Config{
			User:     cfg.User,
			Host:     cfg.Host,
			Port:     cfg.Port,
			StateDir: cfg.StateDir,
		})
	}

	return &Remote{
		kind:    remoteKindSSH,
		display: display,
		ssh:     cfg,
		Dial: func(ctx context.Context) (net.Conn, error) {
			mu.Lock()
			defer mu.Unlock()
			if tunnel == nil {
				t, err := openTunnel(ctx)
				if err != nil {
					return nil, err
				}
				tunnel = t
			}
			conn, err := dialUnixSock(ctx, tunnel.LocalSocket())
			if err == nil {
				return conn, nil
			}
			_ = tunnel.Close()
			tunnel = nil
			t, openErr := openTunnel(ctx)
			if openErr != nil {
				return nil, fmt.Errorf("reopen ssh tunnel after local socket error %v: %w", err, openErr)
			}
			tunnel = t
			return dialUnixSock(ctx, tunnel.LocalSocket())
		},
		Close: func() error {
			mu.Lock()
			defer mu.Unlock()
			if tunnel == nil {
				return nil
			}
			err := tunnel.Close()
			tunnel = nil
			return err
		},
	}
}

func httpClient(r *Remote) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return r.Dial(ctx)
		},
	}}
}

func dialAttachRaw(r *Remote, key string) dialFn {
	return func(ctx context.Context) (net.Conn, error) {
		conn, err := r.Dial(ctx)
		if err != nil {
			return nil, err
		}
		if err := upgradeAttachConn(conn, "http://hoot/sessions/"+key+"/attach-raw"); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

func remoteResponseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("remote: %s: %s", resp.Status, msg)
}

// hostFromHostport returns the host part of host:port for the TLS
// SNI / cert-verify ServerName field.
func hostFromHostport(hp string) string {
	if h, _, err := net.SplitHostPort(hp); err == nil {
		return h
	}
	return hp
}

// ensureRemoteHoot probes the remote for a `hoot` binary at the local
// CLI's compiled-in version. If missing or mismatched, it pipes the
// embedded install.sh through ssh to install/upgrade it, then re-probes.
//
// Errors here surface to the user with the actual reason — no curl on
// remote, no writable bin dir, etc. — instead of becoming an opaque
// tunnel-startup timeout downstream.
//
// Skipped entirely for `dev` builds (no embedded version to compare against).
// Set HOOT_NO_BOOTSTRAP=1 to opt out (e.g. when intentionally targeting a
// different remote version).
func ensureRemoteHoot(ctx context.Context, cfg sshRemoteConfig) error {
	if Version == "dev" {
		return nil
	}
	if os.Getenv("HOOT_NO_BOOTSTRAP") != "" {
		return nil
	}
	remoteVer, err := bootstrap.Probe(ctx, cfg.User, cfg.Host, cfg.Port)
	if err != nil {
		return fmt.Errorf("probe remote hoot: %w", err)
	}
	if remoteVer == Version {
		return nil
	}

	displayVer := remoteVer
	if displayVer == "" {
		displayVer = "<missing>"
	}
	fmt.Fprintf(os.Stderr, "hoot: remote has %s, installing %s\n", displayVer, Version)

	if err := bootstrap.Install(ctx, bootstrap.Options{
		User:    cfg.User,
		Host:    cfg.Host,
		Port:    cfg.Port,
		Mode:    bootstrap.ModeAuto,
		Version: Version,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	}); err != nil {
		return fmt.Errorf("install hoot on remote: %w", err)
	}

	remoteVer, err = bootstrap.Probe(ctx, cfg.User, cfg.Host, cfg.Port)
	if err != nil {
		return fmt.Errorf("post-install probe: %w", err)
	}
	if remoteVer != Version {
		got := remoteVer
		if got == "" {
			got = "<missing>"
		}
		return fmt.Errorf("post-install hoot version is %q, expected %q", got, Version)
	}
	return nil
}
