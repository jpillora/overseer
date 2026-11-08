package overseer

import "os/signal"

// shutdown detaches the supervisor before Run can enter its disabled fallback,
// or RunErr returns control to an application that keeps running. A legacy
// fetcher may still be inside Init/Fetch; stopping prevents it from installing
// anything when it returns, without waiting forever for user code to cooperate.
func (mp *master) shutdown() {
	mp.shutdownOnce.Do(func() {
		mp.restartMux.Lock()
		mp.stopping = true
		mp.disabled = true
		mp.pendingRestart = false
		mp.forceNext = false
		mp.restartMux.Unlock()
		if mp.cancel != nil {
			mp.cancel()
		}
		if mp.done != nil {
			close(mp.done)
		}
		if mp.signals != nil {
			signal.Stop(mp.signals)
			<-mp.signalsDone
		}
		// Finish any in-progress filesystem transition before returning to
		// application code. Hooks inside a transition must return promptly.
		mp.upgradeMux.Lock()
		mp.restartMux.Lock()
		release := mp.activationRelease
		mp.activationRelease = nil
		mp.restartMux.Unlock()
		if release != nil {
			release()
		}
		mp.workerMux.Lock()
		mp.workerCmd = nil
		mp.pendingSignals = nil
		mp.workerMux.Unlock()
		for _, f := range mp.workerExtraFiles {
			if f != nil {
				f.Close()
			}
		}
		mp.workerExtraFiles = nil
		mp.upgradeMux.Unlock()
	})
}

func (mp *master) isStopping() bool {
	mp.restartMux.Lock()
	defer mp.restartMux.Unlock()
	return mp.stopping
}
