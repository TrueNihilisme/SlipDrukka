package winsvc

import (
	"context"
	"time"
)

// SystemWarning is what the admin page shows while a service runs with
// LocalSystem rights because switching to its own account failed.
const SystemWarning = "Служба працює з правами SYSTEM: перевести її на обмежений обліковий запис не вдалося. Причина — в журналі."

// RestartWait is how long Start waits for the restart under the new account.
var RestartWait = time.Minute

// Start runs a service so that nothing listens on the network while the
// process still has LocalSystem rights (0.18.0).
//
// lower is called first and reports whether the service was switched to its
// own account and its restart is on the way. If so, run is not called: the
// process only waits to be stopped, and the restarted service opens the
// ports under the limited account. If the restart does not come within
// RestartWait, run is called after all: printing must not stop because a
// restart got lost.
func Start(ctx context.Context, lower func() (restarting bool), run func(context.Context) error, log func(format string, args ...any)) error {
	if lower() {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(RestartWait):
			log("перезапуск служби не настав за %v — працюю далі під поточним обліковим записом", RestartWait)
		}
	}
	return run(ctx)
}
