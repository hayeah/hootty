package supervisor

import (
	"fmt"
	"testing"
	"time"
)

func TestPollSuccess(t *testing.T) {
	calls := 0
	val, err := Poll(10*time.Millisecond, time.Second, func() (string, bool, error) {
		calls++
		if calls >= 3 {
			return "found", true, nil
		}
		return "", false, nil
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if val != "found" {
		t.Errorf("got %q, want found", val)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 calls, got %d", calls)
	}
}

func TestPollTimeout(t *testing.T) {
	_, err := Poll(10*time.Millisecond, 50*time.Millisecond, func() (int, bool, error) {
		return 0, false, nil
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestPollError(t *testing.T) {
	_, err := Poll(10*time.Millisecond, time.Second, func() (int, bool, error) {
		return 0, false, fmt.Errorf("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestPollImmediateSuccess(t *testing.T) {
	val, err := Poll(10*time.Millisecond, time.Second, func() (string, bool, error) {
		return "immediate", true, nil
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if val != "immediate" {
		t.Errorf("got %q, want immediate", val)
	}
}
