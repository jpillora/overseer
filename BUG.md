# Bug: `OnPanic` callback never fires when worker panics

**Affects:** v1.2.0, v1.3.0
**File:** `proc_master.go` — `handleSignal` / `sendSignal`

## TL;DR

On a panicking worker, the master's signal-proxy path calls `os.Exit(1)`
from its signal-handler goroutine *before* the `fork()` select reaches the
`cmdwait` branch where the `OnPanic` call lives. The callback is never
invoked. v1.3.0's goroutine-vs-`os.Exit` race fix is correct but sits on a
code path that is unreachable for this class of exit.

The same bug also explains why a worker that panics (exit code 2) is
reported to systemd as `status=1/FAILURE`: the `1` is the hardcoded exit
code from `sendSignal`, not the worker's actual code.

## Root cause

`mp.setupSignalling` installs `signal.Notify(signals)` (unfiltered — every
signal is captured). The handler forwards each signal to the worker via
`sendSignal`, and on any forwarding error calls `os.Exit(1)`:

```go
func (mp *master) sendSignal(s os.Signal) {
    if mp.slaveCmd != nil && mp.slaveCmd.Process != nil {
        if err := mp.slaveCmd.Process.Signal(s); err != nil {
            mp.debugf("signal failed (%s), assuming slave process died unexpectedly", err)
            os.Exit(1)
        }
    }
}
```

Since Go 1.14 the runtime uses **SIGURG** (`urgent I/O condition`) to
preempt goroutines. The master's own runtime delivers SIGURG to itself
continuously. `signal.Notify(signals)` captures them all, so every SIGURG
is proxied to the worker. While the worker is alive that succeeds silently.

The instant the worker exits — e.g. from a panic — the next SIGURG the
master's runtime schedules can't be forwarded. `Process.Signal` returns
`os: process already finished`, and the master calls `os.Exit(1)` from
the signal-handler goroutine. That runs before (and in place of) the
`cmdwait` branch in `fork()`, so:

1. `OnPanic` never fires.
2. The exit code to systemd is `1` from that `os.Exit(1)`, not the
   worker's actual exit code (`2` for a Go panic).

## Reproduction (overseer v1.3.0, debug logging enabled)

Trigger a panic in the worker (divide-by-zero in a goroutine, nil
dereference, or explicit `os.Exit(2)` after writing a fake `panic:` header
— all behave identically). Master debug log:

```
[overseer master] starting /path/to/application
[overseer master] proxy signal (urgent I/O condition)
panic: runtime error: integer divide by zero
[overseer master] proxy signal (urgent I/O condition)
[overseer master] signal failed (os: process already finished), assuming worker process died unexpectedly
```

Then `systemd`: `Main process exited, code=exited, status=1/FAILURE`.

No `prog exited with …` debug line from the `cmdwait` branch. A probe
writing to a file from inside the `cmdwait` branch confirms the branch is
unreachable for this class of exit (the probe file appears for graceful
restarts but never for panic exits).

## Why a minimal reproducer can hide this

The tests at `_test.go` and a hand-rolled reproducer (trivial `Program`
that sleeps and panics, `NoRestart: true`) usually see `OnPanic` fire. A
trivial master has very few goroutines and the Go runtime has little
reason to preempt, so SIGURG is rare. The race between the signal-proxy
`os.Exit(1)` and the `cmdwait` branch is won by whichever of SIGURG or
cmd.Wait completion lands first — on a trivial master, cmd.Wait usually
wins; on a real supervisor with many goroutines, the SIGURG flood almost
always wins.

## Suggested fix

Two independent one-liners in `proc_master.go`. Either in isolation likely
resolves the observable symptom; applying both hardens the path.

### 1. Ignore SIGURG in `handleSignal`

SIGURG from the Go runtime is runtime-internal and nothing a worker wants
proxied. Filter it like the existing `"child exited"` guard:

```go
func (mp *master) handleSignal(s os.Signal) {
    if s == mp.RestartSignal {
        go mp.triggerRestart()
        return
    }
    if s.String() == "child exited" {
        return
    }
    if s == syscall.SIGURG {
        return
    }
    // ... rest unchanged
}
```

(SIGPROF is another candidate if anyone ever runs `net/http/pprof` in the
master.)

### 2. Don't `os.Exit` from `sendSignal` when the worker is already dead

A worker that has just exited is the expected state right before `cmdwait`
runs; a failed forward should not short-circuit the normal exit path.

```go
func (mp *master) sendSignal(s os.Signal) {
    if mp.slaveCmd == nil || mp.slaveCmd.Process == nil {
        return
    }
    if err := mp.slaveCmd.Process.Signal(s); err != nil {
        if errors.Is(err, os.ErrProcessDone) {
            return // cmdwait will handle it
        }
        mp.debugf("signal failed (%s)", err)
    }
}
```

(`os.ErrProcessDone` has existed since Go 1.16.)

With both fixes applied, a panicking worker lands in `cmdwait`, `OnPanic`
runs, and `os.Exit(code)` at the end of that branch propagates the real
`code=2` to systemd.

## Why it matters

`OnPanic` exists to let supervisors alert or log on worker crashes. In any
realistic deployment it is silently lossy: the crashes that most need
observability produce nothing. The misleading `status=1/FAILURE` also
makes operators chase the wrong failure mode.
