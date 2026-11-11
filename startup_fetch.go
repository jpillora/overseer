package overseer

import (
	"context"
	"io"
	"time"

	"github.com/jpillora/overseer/fetcher"
)

func (mp *master) lifecycleContext() context.Context {
	if mp.ctx != nil {
		return mp.ctx
	}
	return context.Background()
}

// startupFetch owns the same single fetching goroutine used for normal polling.
// Only reader acquisition is timed. An unbuffered handoff prevents a late offer
// from being installed after startup has already continued.
func (mp *master) startupFetch() error {
	if mp.Fetcher == nil || mp.StartupFetchTimeout < 0 {
		return nil
	}
	mp.fetchStart = make(chan struct{})
	offer := make(chan bool)
	accepted := make(chan struct{})
	finished := make(chan struct{})
	ctx, cancel := context.WithCancel(mp.lifecycleContext())
	timeout := mp.StartupFetchTimeout
	if timeout == 0 {
		timeout = time.Second
	}
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	go func() {
		if err := mp.Fetcher.Init(); err != nil {
			mp.warnf("fetcher init failed: %s", err)
			cancel()
			close(finished)
			return
		}
		mp.printCheckUpdate = true
		if ctx.Err() == nil {
			reader, err := mp.fetchReader(ctx)
			select {
			case offer <- reader != nil && err == nil:
				select {
				case <-accepted:
					mp.processFetched(reader, err, false)
				case <-ctx.Done():
					mp.discardStartupFetch(reader, ctx.Err())
				}
			case <-ctx.Done():
				mp.discardStartupFetch(reader, ctx.Err())
			}
		}
		cancel()
		close(finished)
		select {
		case <-mp.fetchStart:
		case <-mp.lifecycleContext().Done():
			return
		}
		// Do not immediately retry a canceled startup offer. Fetchers also keep
		// their own normal polling interval, including after cancellation.
		if !mp.waitFetchInterval(mp.MinFetchInterval) {
			return
		}
		mp.fetchLoop()
	}()
	select {
	case hasReader := <-offer:
		if time.Now().Before(deadline) {
			timer.Stop()
			close(accepted)
			if hasReader {
				// The stream's own timeout and the master's lifetime now apply.
				select {
				case <-finished:
				case <-mp.lifecycleContext().Done():
					cancel()
					return ErrStopped
				}
			}
		} else {
			cancel()
		}
	case <-finished:
	case <-timer.C:
		cancel()
		mp.debugf("startup update check timed out; starting installed application")
	case <-mp.lifecycleContext().Done():
		cancel()
		return ErrStopped
	}
	if mp.isStopping() {
		return ErrStopped
	}
	return nil
}

func (mp *master) discardStartupFetch(reader io.Reader, err error) {
	if closer, ok := reader.(io.Closer); ok {
		closer.Close()
	}
	if receiver, ok := mp.Fetcher.(fetcher.Result); ok {
		receiver.FetchResult(err)
	}
}

func (mp *master) waitFetchInterval(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-mp.lifecycleContext().Done():
		return false
	case <-timer.C:
		return !mp.isStopping()
	}
}
