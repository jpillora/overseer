package overseer

import "os"

const envReadyFD = "OVERSEER_READY_FD"
const envReadyHandle = "OVERSEER_READY_HANDLE"
const envWorkerBootstrapped = "OVERSEER_WORKER_BOOTSTRAPPED"

// New transport details belong to the immediate worker. Historical role, ID,
// and path variables remain available to existing Program callbacks.
func clearWorkerTransport() {
	for _, name := range []string{
		"OVERSEER_PARENT_HANDLE", "OVERSEER_SHUTDOWN_HANDLE", "OVERSEER_RESTART_HANDLE",
		"OVERSEER_ADDRESSES", envReadyFD, envReadyHandle,
	} {
		_ = os.Unsetenv(name)
	}
}

func clearBootstrapEnvironment() {
	clearWorkerTransport()
	for _, name := range []string{
		envWorkerID, envIsWorker, envSlaveID, envIsSlave, envNumFDs,
		envBinID, envBinPath, envBinCheck, envBinCheckLegacy,
		envWorkerBootstrapped,
	} {
		_ = os.Unsetenv(name)
	}
}
