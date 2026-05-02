package supervisor

import (
	"errors"
	"os"
	"syscall"
)

// Sentinel errors passed as the cause value when Runner.Run cancels
// the Service's ctx in response to a process signal. Services that
// need to distinguish "user hit Ctrl-C" from "systemd asked us to
// shut down" inspect context.Cause(ctx).
var (
	ErrSIGTERM = errors.New("received SIGTERM")
	ErrSIGINT  = errors.New("received SIGINT")
	ErrSIGHUP  = errors.New("received SIGHUP")
)

// causeForSignal maps an incoming os.Signal to the sentinel error
// that Runner.Run uses when cancelling ctx. Unknown signals get a
// generic "received <name>" error so context.Cause is still
// meaningful.
func causeForSignal(sig os.Signal) error {
	switch sig {
	case syscall.SIGTERM:
		return ErrSIGTERM
	case syscall.SIGINT:
		return ErrSIGINT
	case syscall.SIGHUP:
		return ErrSIGHUP
	default:
		return errors.New("received " + sig.String())
	}
}
