package supervisor

// PTY is the terminal transport for a supervised child's stdio.
//
// The shipped impl is *LibghosttyPTY: the supervisor process opens
// its own PTY and runs Ghostty's VT parser (mitchellh/go-libghostty,
// cgo against libghostty-vt) against the byte stream. It contributes
// HTTP routes (/pty/{text,html,vt,stream,input,resize,send-keys}) to
// Supervisor.Mux().
//
// Consumers construct the concrete impl they want and pass it via
// SupervisorConfig.PTY. Services reach it through super.PTY().
//
// There is no Attach(cmd) method: the worker process always starts
// with its stdio pointing at a PTY (the parent `supervise run`
// command opens a PTY before forking the worker). A Service's
// cmd.Start() inherits fd 0/1/2 by default and ends up inside the
// PTY automatically.
type PTY interface {
	// Write sends raw bytes to the PTY master. Passthrough for
	// large pastes and raw escape sequences.
	Write(data []byte) error

	// SendKeys translates tmux-style key names (e.g. "C-a", "Enter",
	// "hello") into the right on-the-wire bytes for the current
	// emulator mode, and writes them. Mode-aware.
	SendKeys(keys ...string) error

	// Capture returns a textual rendering of the current screen.
	// withEscapes=true preserves ANSI color/attribute sequences;
	// false returns plain text.
	Capture(lines int, withEscapes bool) (string, error)

	// Resize informs the underlying PTY + emulator of a new window
	// size. Updates the master winsize and the emulator's grid.
	Resize(cols, rows uint16) error
}
