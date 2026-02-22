package poller

import "time"

// Runner manages a periodic poll loop with graceful shutdown.
type Runner struct {
	stopCh chan struct{}
}

// Init allocates the stop channel. Call in your constructor.
func (r *Runner) Init() {
	r.stopCh = make(chan struct{})
}

// Run calls fn immediately, then every interval until Stop. Blocks.
func (r *Runner) Run(interval time.Duration, fn func()) {
	fn()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			fn()
		case <-r.stopCh:
			return
		}
	}
}

// Stop signals the loop to exit. Safe to call multiple times.
func (r *Runner) Stop() {
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
}

// StopCh returns the channel for custom select logic.
func (r *Runner) StopCh() <-chan struct{} {
	return r.stopCh
}
