package fetcher

import (
	"io"
	"sync"
)

// interface validation
var _ Interface = &Multiple{}

// Multiple uses multiple fetcher.Interface.
//
// e.g.) fetching from both of local file and GitHub.
//   overseer.Config{
//   	Fetcher: &Multiple{
//   		List: []fetcher.Interface{
//   			&fetcher.File{
//   				Path: "/path/to/my_app",
//   			},
//   			&fetcher.Github{
//   				User: "jpillora",
//   				Repo: "overseer",
//   			},
//   	},
//   }
//
type Multiple struct {
	// List for setting multiple fetchers.
	List []Interface

	result          chan fetchResult
	runningStatusMu sync.RWMutex
	runningStatus   map[int]bool
}

// Init initializes Multiple's setting and
// executes other fetchers Init().
func (f *Multiple) Init() error {
	f.runningStatus = make(map[int]bool, len(f.List))
	f.result = make(chan fetchResult)

	for i, v := range f.List {
		f.updateRunningStatus(i, false)
		if err := v.Init(); err != nil {
			return err
		}
	}
	return nil
}

// Fetch executes other fetchers Fetch() and
// waits the result from them via channel.
func (f *Multiple) Fetch() (io.Reader, error) {
	for i, v := range f.List {
		if f.isRunning(i) {
			continue
		}

		go func(i int, v Interface) {
			f.updateRunningStatus(i, true)
			f.result <- doFetch(v)
			f.updateRunningStatus(i, false)
		}(i, v)
	}

	data := f.waitAndGetFetchResult()
	return data.Reader, data.Err
}

// waitAndGetFetchResult receives fetch result from channel.
func (f *Multiple) waitAndGetFetchResult() fetchResult {
	return <-f.result
}

// isRunning checks running state to avoid dulplicate running.
func (f *Multiple) isRunning(num int) bool {
	f.runningStatusMu.RLock()
	defer f.runningStatusMu.RUnlock()
	return f.runningStatus[num]
}

// updateRunningStatus updates the running state of
// given number's fetch method.
func (f *Multiple) updateRunningStatus(num int, status bool) {
	f.runningStatusMu.Lock()
	f.runningStatus[num] = status
	f.runningStatusMu.Unlock()
}

// doFetch executes Fetch().
func doFetch(f Interface) fetchResult {
	r, err := f.Fetch()
	return fetchResult{
		Reader: r,
		Err:    err,
	}
}

// fetchResult is struct for storing result of Fetch()
// to make use of sending/receiving via channel.
type fetchResult struct {
	Reader io.Reader
	Err    error
}
