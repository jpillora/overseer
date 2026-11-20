package fetcher

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPRetryAfterRejectedCandidate(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "candidate")
		if r.Method == "GET" {
			gets.Add(1)
			w.Write([]byte("candidate"))
		}
	}))
	defer srv.Close()
	f := &HTTP{URL: srv.URL, Interval: time.Millisecond}
	if err := f.Init(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		r, err := f.Fetch()
		if err != nil || r == nil {
			t.Fatal("candidate suppressed after rejection", err)
		}
		io.Copy(io.Discard, r)
		r.(io.Closer).Close()
		f.FetchResult(errors.New("sanity or replacement rejected"))
	}
	r, err := f.Fetch()
	if err != nil || r == nil {
		t.Fatal(err)
	}
	r.(io.Closer).Close()
	f.FetchResult(nil)
	r, err = f.Fetch()
	if err != nil || r != nil || gets.Load() != 3 {
		t.Fatalf("accepted candidate not cached: %v gets=%d", err, gets.Load())
	}
}

func TestHTTPRequestDeadlineIncludesHeadersAndBody(t *testing.T) {
	for _, body := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[body], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "HEAD" {
					w.Header().Set("ETag", "new")
					return
				}
				if body {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer srv.Close()
			f := &HTTP{URL: srv.URL, Timeout: 100 * time.Millisecond}
			f.Init()
			start := time.Now()
			r, err := f.Fetch()
			if err == nil && r != nil {
				_, err = io.Copy(io.Discard, r)
				r.(io.Closer).Close()
			}
			if err == nil {
				t.Fatal("stalled response succeeded")
			}
			if time.Since(start) > time.Second {
				t.Fatal("request deadline not enforced")
			}
		})
	}
}

func TestHTTPNoValidatorAndGzipFailureRetry(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.Write([]byte("not gzip"))
			}
		}))
		url := srv.URL
		if compressed {
			url += "/binary.gz"
		}
		f := &HTTP{URL: url, Interval: time.Millisecond}
		f.Init()
		r, err := f.Fetch()
		if compressed {
			if err == nil {
				t.Fatal("invalid gzip accepted")
			}
		} else {
			if err != nil || r == nil {
				t.Fatal("file without validators was skipped", err)
			}
			r.(io.Closer).Close()
		}
		f.FetchResult(err)
		srv.Close()
	}
}

func TestHTTPRollbackResetReoffersAcceptedCandidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "unchanged")
		if r.Method == "GET" {
			w.Write([]byte("candidate"))
		}
	}))
	defer srv.Close()
	f := &HTTP{URL: srv.URL, Interval: time.Millisecond}
	f.Init()
	r, err := f.Fetch()
	if err != nil || r == nil {
		t.Fatal(err)
	}
	r.(io.Closer).Close()
	f.FetchResult(nil)
	r, err = f.Fetch()
	if err != nil || r != nil {
		t.Fatal("accepted candidate was not cached", err)
	}
	f.Reset()
	r, err = f.Fetch()
	if err != nil || r == nil {
		t.Fatal("rolled-back offer suppressed", err)
	}
	r.(io.Closer).Close()
}
