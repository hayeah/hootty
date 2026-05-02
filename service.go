package supervisor

import "context"

// Service is what the supervisor runs. Consumers implement this.
//
// Run blocks until the service is done. Returning terminates the
// supervisor process. The supervisor owns infra (flock, state.json,
// signal→ctx bridge, unix socket); the service owns policy (spawn
// its child, decide when/whether to restart, handle shutdown signals
// via ctx cancellation).
//
// Services typically:
//   - Publish initial state via super.UpdateState as the first thing
//     inside Run, so external readers see something sane.
//   - Register service-specific HTTP routes on super.Mux().
//   - Spawn their child with cmd.Start(); default stdio inheritance
//     puts the child inside the supervisor's PTY.
//   - Observe ctx.Done() for shutdown; inspect context.Cause(ctx) to
//     distinguish SIGTERM / SIGINT / SIGHUP if it matters.
type Service interface {
	Run(ctx context.Context, super Supervisor) error
}
