package fetcher

import (
	"context"
	"io"
	"time"
)

// Interface defines the required fetcher functions
type Interface interface {
	//Init should perform validation on fields. For
	//example, ensure the appropriate URLs or keys
	//are defined or ensure there is connectivity
	//to the appropriate web service.
	Init() error
	//Fetch should check if there is an updated
	//binary to fetch, and then stream it back the
	//form of an io.Reader. If io.Reader is nil,
	//then it is assumed there are no updates. Fetch
	//will be run repeatedly and forever. It is up the
	//implementation to throttle the fetch frequency.
	Fetch() (io.Reader, error)
}

// ContextFetcher optionally supports canceling an update check, including its
// polling delay and returned stream. Overseer uses it instead of Fetch when
// available. The startup timeout cancels the context only if no reader was
// accepted; an accepted stream can finish downloading beyond that timeout.
// Init, FetchContext, FetchResult, and Reset are serialized on one goroutine.
type ContextFetcher interface {
	FetchContext(context.Context) (io.Reader, error)
}

func wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Result is an optional acknowledgement implemented by caching fetchers.
// Overseer calls it on the fetching goroutine after processing a fetch. A nil
// error means the content was accepted on disk (possibly already present), not
// that a worker became healthy. An error must allow the same offer to be retried.
type Result interface {
	FetchResult(error)
}

// Resetter optionally clears cached offers after activation rollback. Overseer
// calls Reset on the fetching goroutine, never concurrently with Init or Fetch.
// The next fetch can retry the same offer after its normal polling interval.
type Resetter interface{ Reset() }

// Func converts a fetch function into the fetcher interface
func Func(fn func() (io.Reader, error)) Interface {
	return &fetcher{fn}
}

type fetcher struct {
	fn func() (io.Reader, error)
}

func (f fetcher) Init() error {
	return nil //skip
}

func (f fetcher) Fetch() (io.Reader, error) {
	return f.fn()
}
