package supervisor

import (
	"fmt"
	"time"
)

// Poll retries fn at the given interval until it returns (value, true)
// or the timeout elapses. Returns the value on success or an error on
// timeout.
func Poll[T any](interval, timeout time.Duration, fn func() (T, bool, error)) (T, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Try once immediately
	if v, ok, err := fn(); err != nil {
		var zero T
		return zero, err
	} else if ok {
		return v, nil
	}

	for {
		select {
		case <-ticker.C:
			if time.Now().After(deadline) {
				var zero T
				return zero, fmt.Errorf("poll timed out after %s", timeout)
			}
			v, ok, err := fn()
			if err != nil {
				var zero T
				return zero, err
			}
			if ok {
				return v, nil
			}
		}
	}
}
