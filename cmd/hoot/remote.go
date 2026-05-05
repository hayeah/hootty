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
	return &Remote{
		kind:    remoteKindSSH,
		display: display,
		ssh:     cfg,
		Dial: func(context.Context) (net.Conn, error) {
			return nil, fmt.Errorf("ssh remote transport not wired yet")
		},
		Close: func() error { return nil },
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
