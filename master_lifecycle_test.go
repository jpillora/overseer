package overseer

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type lateFetcher struct {
	entered, resume chan struct{}
	reader          io.Reader
	result          chan error
}

func (*lateFetcher) Init() error                 { return nil }
func (f *lateFetcher) Fetch() (io.Reader, error) { close(f.entered); <-f.resume; return f.reader, nil }
func (f *lateFetcher) FetchResult(err error)     { f.result <- err }

type closingReader struct {
	*bytes.Reader
	closed bool
}

func (r *closingReader) Close() error { r.closed = true; return nil }

func TestShutdownDiscardsLateFetchBeforeValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server"+extension())
	if err := os.WriteFile(path, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}
	r := &closingReader{Reader: bytes.NewReader([]byte("candidate"))}
	f := &lateFetcher{entered: make(chan struct{}), resume: make(chan struct{}), reader: r, result: make(chan error, 1)}
	mp := &master{Config: &Config{Fetcher: f, PreUpgrade: func(string) error { t.Error("late fetch ran validation"); return nil }, OnUpgrade: func(UpgradeEvent) { t.Error("disabled supervisor reported an event") }}, binPath: path, binPerms: 0755}
	done := make(chan struct{})
	go func() { mp.fetch(); close(done) }()
	<-f.entered
	mp.shutdown()
	close(f.resume)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("late fetch stuck")
	}
	if !r.closed || !errors.Is(<-f.result, ErrStopped) {
		t.Fatal("late fetch was not rejected and closed")
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "original" {
		t.Fatal("late fetch changed executable", err)
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatal("late fetch created staging files", err)
	}
}
