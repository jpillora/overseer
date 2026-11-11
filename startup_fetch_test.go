package overseer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type startupTestFetcher struct {
	init   func() error
	fetch  func() (io.Reader, error)
	result func(error)
}

func (f *startupTestFetcher) Init() error {
	if f.init != nil {
		return f.init()
	}
	return nil
}
func (f *startupTestFetcher) Fetch() (io.Reader, error) { return f.fetch() }
func (f *startupTestFetcher) FetchResult(err error) {
	if f.result != nil {
		f.result(err)
	}
}

func startupTestMaster(t *testing.T, f *startupTestFetcher) *master {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server"+extension())
	if err := os.WriteFile(path, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	hash, err := binaryHash(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	mp := &master{Config: &Config{Fetcher: f, StartupFetchTimeout: 50 * time.Millisecond, MinFetchInterval: 50 * time.Millisecond, NoWarn: true}, ctx: ctx, cancel: cancel, done: make(chan struct{}), binPath: path, binHash: hash, binPerms: 0755}
	t.Cleanup(mp.shutdown)
	return mp
}

func TestStartupTimeoutDiscardsLateLegacyReader(t *testing.T) {
	resume := make(chan struct{})
	results := make(chan error, 2)
	second := make(chan struct{})
	var calls atomic.Int32
	r := &closingReader{Reader: bytes.NewReader([]byte("late candidate"))}
	f := &startupTestFetcher{fetch: func() (io.Reader, error) {
		call := calls.Add(1)
		if call == 1 {
			<-resume
			return r, nil
		}
		if call == 2 {
			close(second)
		}
		return nil, nil
	}, result: func(err error) {
		select {
		case results <- err:
		default:
		}
	}}
	mp := startupTestMaster(t, f)
	mp.PreUpgrade = func(string) error { t.Error("late reader reached validation"); return nil }
	start := time.Now()
	if err := mp.startupFetch(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < mp.StartupFetchTimeout || elapsed > time.Second {
		t.Fatalf("startup wait was not bounded: %s", elapsed)
	}
	close(resume)
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) || !r.closed {
			t.Fatal("late reader was not closed and rejected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late result was not acknowledged")
	}
	if calls.Load() != 1 {
		t.Fatal("polling started before worker activation")
	}
	data, err := os.ReadFile(mp.binPath)
	if err != nil || string(data) != "original" {
		t.Fatal("timed-out offer changed installed binary", err)
	}
	mp.startFetching()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("normal polling did not resume")
	}
	mp.shutdown()
}

func TestStartupTimeoutIncludesLegacyInit(t *testing.T) {
	resume, initialized := make(chan struct{}), make(chan struct{})
	var fetches atomic.Int32
	mp := startupTestMaster(t, &startupTestFetcher{
		init:  func() error { <-resume; close(initialized); return nil },
		fetch: func() (io.Reader, error) { fetches.Add(1); return nil, nil },
	})
	if err := mp.startupFetch(); err != nil {
		t.Fatal(err)
	}
	close(resume)
	<-initialized
	if fetches.Load() != 0 {
		t.Fatal("canceled startup check called Fetch after late Init")
	}
}

type gatedStartupReader struct {
	*bytes.Reader
	entered, resume chan struct{}
	closed          chan struct{}
}

func (r *gatedStartupReader) Read(p []byte) (int, error) {
	if r.entered != nil {
		close(r.entered)
		r.entered = nil
	}
	<-r.resume
	return r.Reader.Read(p)
}
func (r *gatedStartupReader) Close() error { close(r.closed); return nil }

func TestStartupAcceptedReaderCanFinishBeyondTimeout(t *testing.T) {
	r := &gatedStartupReader{Reader: bytes.NewReader([]byte("original")), entered: make(chan struct{}), resume: make(chan struct{}), closed: make(chan struct{})}
	entered := r.entered
	mp := startupTestMaster(t, &startupTestFetcher{fetch: func() (io.Reader, error) { return r, nil }})
	// The reader's payload matches the installed binary, so this tests complete
	// streaming without requiring a candidate subprocess.
	done := make(chan error, 1)
	go func() { done <- mp.startupFetch() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("startup stream not read")
	}
	select {
	case err := <-done:
		t.Fatal("accepted download was cut off by startup timeout", err)
	case <-time.After(3 * mp.StartupFetchTimeout):
	}
	close(r.resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup did not continue after download")
	}
	<-r.closed
}

func TestStartupNoReaderDoesNotWaitForTimeoutOrResultHook(t *testing.T) {
	resume, result := make(chan struct{}), make(chan struct{})
	mp := startupTestMaster(t, &startupTestFetcher{fetch: func() (io.Reader, error) { return nil, nil }, result: func(error) { close(result); <-resume }})
	mp.StartupFetchTimeout = time.Minute
	done := make(chan error, 1)
	go func() { done <- mp.startupFetch() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(resume)
		t.Fatal("no-update check held up startup")
	}
	<-result
	close(resume)
}

func TestStartupStopCancelsWait(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			entered, resume := make(chan struct{}), make(chan struct{})
			mp := startupTestMaster(t, &startupTestFetcher{fetch: func() (io.Reader, error) { close(entered); <-resume; return nil, nil }})
			mp.StartupFetchTimeout = time.Minute
			done := make(chan error, 1)
			go func() { done <- mp.startupFetch() }()
			<-entered
			mp.handleSignal(sig)
			select {
			case err := <-done:
				if !errors.Is(err, ErrStopped) {
					t.Fatal("termination did not stop startup", err)
				}
			case <-time.After(time.Second):
				t.Fatal("termination stuck behind startup fetch")
			}
			close(resume)
		})
	}
}

func TestStartupFetchCanBeDisabled(t *testing.T) {
	mp := startupTestMaster(t, &startupTestFetcher{init: func() error { t.Error("disabled startup check initialized fetcher"); return nil }})
	mp.StartupFetchTimeout = -1
	if err := mp.startupFetch(); err != nil || mp.fetchStart != nil {
		t.Fatal("disabled startup check did not skip fetching", err)
	}
}
