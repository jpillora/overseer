# TASKS

a [meads](https://github.com/jpillora/meads) (`md`) managed task log

* created: 2026-06-12T07:25:25Z
* updated: 2026-06-12T07:32:04Z

## 1. Add ForceNextRestart() to bypass ShouldRestart for operator-mandated upgrades

* status: closed
* priority: P2
* type: feature
* status-reason: ForceNextRestart() implemented; consumed at restart attempt and on fork
* created: 2026-06-12T07:25:25Z
* updated: 2026-06-12T07:32:04Z

The next restart attempt, typically after a successful fetch, proceeds regardless of `Config.ShouldRestart`. The master clears the request when the restart fires. Like `Restart()`, this package function is valid in the master process. Applications can request a forced activation without keeping a separate flag in their `ShouldRestart` callback.

## 2. Expose master introspection: MasterInfo() with UpgradeStaged/RestartPending/ForceRequested/WorkerForks

* status: closed
* priority: P2
* type: feature
* status-reason: MasterInfo() with UpgradeStaged/RestartPending/ForceRequested/WorkerForks
* created: 2026-06-12T07:25:25Z
* updated: 2026-06-12T07:32:04Z

The master already tracks `binHash`, `workerID`, and `pendingRestart`, and passes `envBinID` to workers at fork. Capture the worker's hash at fork and expose `UpgradeStaged`, `RestartPending`, `ForceRequested`, and `WorkerForks` through `MasterInfo()`. This lets applications distinguish a staged binary from the running worker using hashes, without exchanging version strings across processes.
