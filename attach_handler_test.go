package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

func TestStreamAsciiCinemaPlaybackStripsQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pty.cast")
	rec, err := NewRecorder(path, 80, 24)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if _, err := rec.Write([]byte("hello\x1b[cworld")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var got strings.Builder
	err = streamAsciiCinemaPlayback(path, asciiCinemaPlaybackConfig{
		Enabled: true,
		Window:  5 * time.Minute,
		Speed:   1000,
	}, func(chunk []byte) {
		got.Write(chunk)
	})
	if err != nil {
		t.Fatalf("streamAsciiCinemaPlayback: %v", err)
	}
	if got.String() != "helloworld" {
		t.Fatalf("playback output = %q, want query stripped", got.String())
	}
}

func TestAsciiCinemaPlaybackConfigDefaultsAndOverrides(t *testing.T) {
	cfg := asciiCinemaPlaybackConfigFromHello(zeroHello())
	if !cfg.Enabled || cfg.Window != defaultAsciiCinemaPlaybackWindow || cfg.Speed != defaultAsciiCinemaPlaybackSpeed {
		t.Fatalf("default cfg = %+v", cfg)
	}

	enabled := false
	window := 0.0
	speed := 2.5
	cfg = asciiCinemaPlaybackConfigFromHello(helloWithPlayback(&enabled, &window, &speed))
	if cfg.Enabled || cfg.Window != 0 || cfg.Speed != 2.5 {
		t.Fatalf("override cfg = %+v", cfg)
	}
}

func zeroHello() attachwire.Hello {
	return attachwire.Hello{}
}

func helloWithPlayback(enabled *bool, window, speed *float64) attachwire.Hello {
	return attachwire.Hello{
		AsciiCinemaPlayback:              enabled,
		AsciiCinemaPlaybackWindowSeconds: window,
		AsciiCinemaPlaybackSpeed:         speed,
	}
}
