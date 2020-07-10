package fetcher

import (
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
	hash  string
	delay bool
}

// Init sets the Path and Interval options
func (f *File) Init() error {
	if f.Path == "" {
		return errors.New("path required")
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
	//only delay after first fetch
	if f.delay {
		time.Sleep(f.Interval)
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
		time.Sleep(rate)
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
	return file, nil
}

func (f *File) updateHash() error {
	file, err := os.Open(f.Path)
	if err != nil {
		//binary does not exist, skip
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open file error: %w", err)
	}
	defer file.Close()
	s, err := file.Stat()
	if err != nil {
		return fmt.Errorf("get file stat error: %w", err)
	}
	f.hash = fmt.Sprintf("%d|%d", s.ModTime().UnixNano(), s.Size())
	return nil
}
