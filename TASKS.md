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

Master-side latch: the next restart attempt (typically after the next successful fetch) proceeds regardless of Config.ShouldRestart; cleared once a restart actually fires. Exported package func valid in the master process, like Restart(). Motivation: rais md#493 manual /apply currently keeps an applyRequested latch inside its UpdateDaemon and special-cases it in its ShouldRestart closure — restart policy belongs in overseer.

## 2. Expose master introspection: MasterInfo() with UpgradeStaged/RestartPending/ForceRequested/WorkerForks

* status: closed
* priority: P2
* type: feature
* status-reason: MasterInfo() with UpgradeStaged/RestartPending/ForceRequested/WorkerForks
* created: 2026-06-12T07:25:25Z
* updated: 2026-06-12T07:32:04Z

Master already tracks binHash, workerID, pendingRestart, and passes envBinID to workers at fork. Capture the forked-from hash in fork() and expose Info{UpgradeStaged (disk binHash != forked-from hash), RestartPending, ForceRequested, WorkerForks}. Master-process only. Motivation: lets embedders (rais update daemon) report staged-vs-running truthfully by hash instead of round-tripping version strings (rais ships X-Rais-Worker-Version header machinery just for this).
