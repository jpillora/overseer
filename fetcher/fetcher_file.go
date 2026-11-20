package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// File checks the provided Path, at the provided
// Interval for new Go binaries. When a new binary
// is found it will replace the currently running
// binary.
type File struct {
	Path     string
	Interval time.Duration
	// hash is the file modify time and its size
	hash         string
	previousHash string
	delay        bool
}

// Init sets the Path and Interval options
func (f *File) Init() error {
	if f.Path == "" {
		return fmt.Errorf("Path required")
	}
	if f.Interval < 1*time.Second {
		f.Interval = 1 * time.Second
	}
	if err := f.updateHash(); err != nil {
		return err
	}
	return nil
}

// Fetch file from the specified Path
func (f *File) Fetch() (io.Reader, error) {
	return f.FetchContext(context.Background())
}

// FetchContext is Fetch with cancelable polling and file-stability waits.
func (f *File) FetchContext(ctx context.Context) (io.Reader, error) {
	f.previousHash = f.hash
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	//only delay after first fetch
	if f.delay {
		if err := wait(ctx, f.Interval); err != nil {
			return nil, err
		}
	}
	f.delay = true
	lastHash := f.hash
	if err := f.updateHash(); err != nil {
		return nil, err
	}
	// no change
	if lastHash == f.hash {
		return nil, nil
	}
	// changed!
	file, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}
	//check every 1/4s for 5s to
	//ensure its not mid-copy
	const rate = 250 * time.Millisecond
	const total = int(5 * time.Second / rate)
	attempt := 1
	for {
		if attempt == total {
			file.Close()
			return nil, errors.New("file is currently being changed")
		}
		attempt++
		//sleep
		if err := wait(ctx, rate); err != nil {
			file.Close()
			return nil, err
		}
		//check hash!
		if err := f.updateHash(); err != nil {
			file.Close()
			return nil, err
		}
		//check until no longer changing
		if lastHash == f.hash {
			break
		}
		lastHash = f.hash
	}
	if ctx.Done() == nil {
		return file, nil
	}
	return &contextFile{File: file, stop: context.AfterFunc(ctx, func() { file.Close() })}, nil
}

type contextFile struct {
	*os.File
	stop func() bool
}

func (f *contextFile) Close() error { f.stop(); return f.File.Close() }

func (f *File) updateHash() error {
	file, err := os.Open(f.Path)
	if err != nil {
		//binary does not exist, skip
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("Open file error: %s", err)
	}
	defer file.Close()
	s, err := file.Stat()
	if err != nil {
		return fmt.Errorf("Get file stat error: %s", err)
	}
	f.hash = fmt.Sprintf("%d|%d", s.ModTime().UnixNano(), s.Size())
	return nil
}

func (f *File) FetchResult(err error) {
	if err != nil {
		f.hash = f.previousHash
	}
}

func (f *File) Reset() { f.hash = "" }
