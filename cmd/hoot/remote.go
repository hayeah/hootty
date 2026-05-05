package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

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
		raw = "http://" + raw
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
	openTunnel := func(ctx context.Context) (*sshtransport.Tunnel, error) {
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
