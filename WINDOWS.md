# Windows upgrades

Windows uses inherited, unnamed kernel events for master-to-worker shutdown and
worker-to-master restart requests. Each request is tied to the originating
worker, including requests waiting behind an upgrade. The worker watches an
inherited handle to the actual master process and exits when that process dies.
Bootstrap handles are non-inheritable in the worker, and private bootstrap
environment variables are removed before calling `Program`. Independently
launched child applications therefore start their own overseer master.

## Startup and polling

The first worker starts before the fetcher is initialized. A stalled legacy
`Init`, `Fetch`, or returned reader cannot prevent application startup. Fetching
uses one goroutine: an uncooperative custom fetcher can stall further downloads,
but it does not create repeated abandoned goroutines. Retries of an already
staged restart run separately from polling.

HTTP and Github fetchers have a configurable `Timeout`, defaulting to five
minutes per complete request, including response-body reads. S3 uses its existing
`HeadTimeout` (five seconds) for checks and credential metadata, and `GetTimeout`
(five minutes) for downloads. Custom fetchers should provide equivalent bounds.
The S3 metadata fallback retains IMDSv1 behavior; IMDSv2-only deployments should
supply credentials through the environment or the fetcher configuration.

## Replacement and recovery

Candidates are staged beside the installed executable, checked by running the
candidate, authorized, and renamed into place. Each replacement uses a unique
backup name: the original master image remains mapped across multiple upgrades,
so reusing one backup name would prevent later updates. A failed replacement
restores the previous executable. Mapped backups remain until Windows permits
deletion; subsequent replacements and master starts collect them.

`Config.SanityCheckTimeout` defaults to 30 seconds on Windows and five seconds
elsewhere, allowing first-run executable scanning. Validation only establishes
that the candidate understands overseer's bootstrap protocol.

For application-health recovery, opt in to `Config.ReadyTimeout` and call
`State.Ready()` after initialization on **every** worker start. The master keeps
a separate copy of the acknowledged executable, including across multiple staged
updates while busy. A candidate that fails to start, exits before readiness, or
exceeds the readiness timeout is terminated and replaced with that copy. The
same installer lock covers activation, restoration, and the recovery worker's
readiness. Explicit termination stops the service instead of starting recovery.
If the candidate disappears after activation approval, restoration can install
the retained image into the now-empty executable path.
An initial worker without a previous acknowledged image cannot be rolled back.
Recovery failure returns an error; it does not loop indefinitely.
`RunErr` returns `ErrStopped` when termination interrupts startup or recovery;
`Run` exits cleanly for that outcome and never launches its disabled fallback.
On other failures, `RunErr` detaches master-owned signal forwarding, polling,
installer leases, and listener descriptors before returning. With `Required`
disabled, `Run` can then invoke `Program(DisabledState)` with normal application
signal handling. `MasterInfo` returns zero and restart/force calls become no-ops
in that fallback. Set `Required: true` when an unsupervised fallback is unwanted.
Shutdown cancels sanity commands and discards late fetch results before further
validation. Already-running user callbacks/readers still need to cooperate;
shutdown does not wait forever for a stalled legacy fetcher.

With readiness disabled, existing programs retain their behavior: successful
`cmd.Start` counts as activation. Readiness cannot detect errors after
acknowledgement, undo application data migrations, or fix external services.
`AuthorizeUpgrade` and `UpgradeLock` still apply to forced/manual activation;
restoring the previously acknowledged image retains its existing authorization.
External replacements cannot obtain a rollback copy of an image already removed
from disk; readiness recovery covers candidates installed by this master.

Replacement and rollback handle process-level failures. They are not a durable
transaction across power loss or a master crash between filesystem operations.
If a crash interrupts the two renames, the previous executable may need to be
restored from its adjacent backup. Retained rollback copies are not a substitute
for an application's backup or installer recovery strategy.

## Upgrade outcomes

Downloading and staging are separate from activation. `Config.OnUpgrade` reports
`rejected`, `staged`, `activated`, and `rolled_back`, using the binary's SHA-1
content ID (the same identifier as `State.ID`). IDs identify content; they do not
authenticate it. Only `activated` acknowledges a working application when
readiness is enabled. `MasterInfo` exposes `ActiveID` and `StagedID` separately.
The master also emits initial activation; a recovery of the same active ID does
not emit another activation event.

Callbacks run outside overseer's locks and may run concurrently. Their
`Sequence` numbers reflect transition order in this master: consumers should
ignore older sequences when updating a single current status. Keep callbacks
short, and do not call `Restart` synchronously from them. Slow fetching-side
callbacks delay subsequent polling; slow activation-side callbacks delay worker
exit observation. Sequences restart when the master process restarts.

Caching fetchers can implement `fetcher.Result` to acknowledge accepted content
or allow retry after validation, authorization, or installation failure. Built-in
fetchers implement it. They also implement `fetcher.Resetter`: rollback clears
cached metadata on the fetch goroutine, allowing the same offer to be retried
at its normal polling interval. A persistently unhealthy offer can therefore
fail again; publishers should withdraw broken updates. Custom fetchers should
track offered, staged, and active versions separately and use activation outcomes
rather than advancing the active version when returning a download reader.

## User experience and compatibility

`Config.Address` and `Config.Addresses` use listener rebinding on Windows. The
old worker exits before its replacement opens the same addresses, leaving a
brief listening gap. Programs opening their own listeners use the same sequence.
Existing connections drain within `TerminateTimeout`; new connection attempts
during the gap may fail and need retrying. Unix retains listener descriptor
inheritance and overlapping workers.

There are no update prompts or elevation requests added by overseer. The account
running the master needs permission to modify the executable directory. Windows
sharing restrictions and scanners can defer replacement without losing the
installed image. Manual restarts bypass `ShouldRestart`; upgrade authorization
remains mandatory. Ctrl+C and termination stop the service.
`ShouldRestart` may inspect `MasterInfo` and call `ForceNextRestart`. User policy
runs outside the restart state mutex; stop and force requests are checked again
before claiming the restart. Policy callbacks must return promptly and must not
call `Restart` synchronously.

The master keeps its original configuration and code until it is explicitly
restarted. Older Windows masters do not provide the new event handles, so moving
to this Windows protocol requires one normal master restart/manual replacement.
Readiness is optional for existing applications, but every candidate offered to
a readiness-enabled master must support `State.Ready()`.

## Standalone verification

Run on native Windows from this checkout:

```powershell
go test -v ./...
```

Run Linux regressions with:

```sh
go test -race ./...
```

The standalone suite builds real executable versions and checks repeated
upgrades, both listener modes, Unicode/spaces/shell metacharacters in paths and
arguments, invalid candidates, denied authorization, busy/forced activation,
worker-requested restart, graceful cleanup, hung-worker termination, master-death
cleanup, stalled fetcher startup, independent descendant launch, readiness
exit/timeout/bind failure, rollback followed by another upgrade, stopping during
readiness/recovery, and installer-lock retention. Native Windows tests also
exercise a stale kernel restart event, an actual file-sharing denial, and
candidate removal during old-worker shutdown. Regressions cover predicate
status/force calls, termination during policy evaluation, fallback listener and
signal cleanup, late fetch rejection, and sanity-command cancellation. The
fallback test sends real SIGTERM on Unix; Windows checks supervisor cleanup
before terminating its subprocess.

Qualification completed on Windows 11 amd64 (`go test -v ./...`) and Linux
(`go test -race ./...`). Windows ARM64 tests cross-compile; they have not been
executed on ARM64 hardware. The legacy WMI query API was also checked on native
Windows after updating its COM dependency for ARM64 compilation support.
