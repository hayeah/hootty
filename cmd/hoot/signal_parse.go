package main

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
)

// signalsByName maps the standard Unix signal names (without the SIG
// prefix) to syscall.Signal values. Covers the POSIX-mandated set
// plus a handful of widely-used non-POSIX ones (USR1/2, WINCH, IO,
// CHLD, CONT, STOP, TSTP). All members are also present on Linux
// and Darwin, the two platforms hoot targets.
var signalsByName = map[string]syscall.Signal{
	"HUP":    syscall.SIGHUP,
	"INT":    syscall.SIGINT,
	"QUIT":   syscall.SIGQUIT,
	"ILL":    syscall.SIGILL,
	"TRAP":   syscall.SIGTRAP,
	"ABRT":   syscall.SIGABRT,
	"IOT":    syscall.SIGIOT,
	"BUS":    syscall.SIGBUS,
	"FPE":    syscall.SIGFPE,
	"KILL":   syscall.SIGKILL,
	"USR1":   syscall.SIGUSR1,
	"SEGV":   syscall.SIGSEGV,
	"USR2":   syscall.SIGUSR2,
	"PIPE":   syscall.SIGPIPE,
	"ALRM":   syscall.SIGALRM,
	"TERM":   syscall.SIGTERM,
	"CHLD":   syscall.SIGCHLD,
	"CONT":   syscall.SIGCONT,
	"STOP":   syscall.SIGSTOP,
	"TSTP":   syscall.SIGTSTP,
	"TTIN":   syscall.SIGTTIN,
	"TTOU":   syscall.SIGTTOU,
	"URG":    syscall.SIGURG,
	"XCPU":   syscall.SIGXCPU,
	"XFSZ":   syscall.SIGXFSZ,
	"VTALRM": syscall.SIGVTALRM,
	"PROF":   syscall.SIGPROF,
	"WINCH":  syscall.SIGWINCH,
	"IO":     syscall.SIGIO,
	"SYS":    syscall.SIGSYS,
}

// parseSignal accepts a kill(1)-style spec and returns the
// corresponding syscall.Signal:
//
//	"TERM", "sigterm", "SIGTERM" → syscall.SIGTERM
//	"9", "15"                    → syscall.SIGKILL, syscall.SIGTERM
//
// Empty input is rejected — callers should default to "TERM" before
// calling.
func parseSignal(spec string) (syscall.Signal, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return 0, fmt.Errorf("empty signal")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 || n > 64 {
			return 0, fmt.Errorf("signal number %d out of range (1..64)", n)
		}
		return syscall.Signal(n), nil
	}
	upper := strings.ToUpper(s)
	upper = strings.TrimPrefix(upper, "SIG")
	if sig, ok := signalsByName[upper]; ok {
		return sig, nil
	}
	return 0, fmt.Errorf("unknown signal %q", spec)
}
