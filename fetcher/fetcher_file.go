package fetcher

import (
	"fmt"
	"io"
	"os"
	"time"
)

// File is used to check new version
type File struct {
	Path     string
	Interval time.Duration
	// file modify time and its size makes up its hash
	uniqHash string
	delay    bool
}

// Init interval and lastHash
func (f *File) Init() error {
	if f.Path == "" {
		return fmt.Errorf("Path required")
	}
	if f.Interval == 0 {
		f.Interval = 10 * time.Second
	}

	if err := f.updateHash(); err != nil {
		return err
	}
	f.delay = false

	return nil
}

// Fetch file
func (f *File) Fetch() (io.Reader, error) {
	//delay fetches after first
	if f.delay {
		time.Sleep(f.Interval)
	}
	f.delay = true

	lastHash := f.uniqHash
	if err := f.updateHash(); err != nil {
		return nil, err
	}
	// no change
	if lastHash == f.uniqHash {
		return nil, nil
	}

	file, err := os.Open(f.Path)
	if err != nil {
		return nil, err
	}

	return file, nil
}

func (f *File) updateHash() error {
	file, err := os.Open(f.Path)
	if err != nil {
		// new version not exist, return
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("Open file error: %s", err)
	}
	defer file.Close()

	state, err := file.Stat()
	if err != nil {
		return fmt.Errorf("Get file state error: %s", err)
	}

	n := state.ModTime().UnixNano()
	s := state.Size()
	f.uniqHash = fmt.Sprintf("%d%d", n, s)

	return nil
}
