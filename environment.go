package overseer

import "os"

const envReadyFD = "OVERSEER_READY_FD"
const envReadyHandle = "OVERSEER_READY_HANDLE"

// Bootstrap state belongs to this worker, never its independently launched
// descendants. Remove exact private names, preserving application OVERSEER_*.
func clearBootstrapEnvironment() {
	for _, name := range []string{
		envWorkerID, envIsWorker, envSlaveID, envIsSlave, envNumFDs,
		envBinID, envBinPath, envBinCheck, envBinCheckLegacy,
		"OVERSEER_PARENT_HANDLE", "OVERSEER_SHUTDOWN_HANDLE",
		"OVERSEER_RESTART_HANDLE", "OVERSEER_ADDRESSES", envReadyFD, envReadyHandle,
	} {
		_ = os.Unsetenv(name)
	}
}
