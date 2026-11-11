package fetcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextCancelsPollingWithoutLosingAcceptedCache(t *testing.T) {
	h := &HTTP{Interval: time.Hour, delay: true, lasts: map[string]string{"ETag": "accepted"}}
	g := &Github{Interval: time.Hour, delay: true, lastETag: "accepted"}
	s := &S3{Interval: time.Hour, delay: true, lastETag: "accepted"}
	f := &File{Interval: time.Hour, delay: true, hash: "accepted"}
	for _, test := range []struct {
		name string
		f    interface {
			ContextFetcher
			Result
		}
		cached func() string
	}{
		{"HTTP", h, func() string { return h.lasts["ETag"] }},
		{"Github", g, func() string { return g.lastETag }},
		{"S3", s, func() string { return s.lastETag }},
		{"File", f, func() string { return f.hash }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			start := time.Now()
			r, err := test.f.FetchContext(ctx)
			if r != nil || !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
				t.Fatal("polling did not cancel", err)
			}
			test.f.FetchResult(err)
			if test.cached() != "accepted" {
				t.Fatal("canceled poll discarded accepted offer")
			}
		})
	}
}

func TestHTTPContextCancelsRequests(t *testing.T) {
	for _, stage := range []string{"HEAD", "GET"} {
		t.Run(stage, func(t *testing.T) {
			entered := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", "candidate")
				if r.Method == stage {
					close(entered)
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			f := &HTTP{URL: srv.URL}
			f.Init()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				r, err := f.FetchContext(ctx)
				if r != nil {
					r.(io.Closer).Close()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request not started")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("request not canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request cancellation stuck")
			}
		})
	}
}

func TestGithubContextCancelsEveryRequestStage(t *testing.T) {
	for _, stage := range []string{"release", "location", "range", "binary"} {
		t.Run(stage, func(t *testing.T) {
			entered := make(chan struct{})
			var serverURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				current := "binary"
				if r.URL.Path == "/release" {
					current = "release"
				} else if r.Method == "HEAD" {
					current = "location"
				} else if r.Header.Get("Range") != "" {
					current = "range"
				}
				if current == stage {
					close(entered)
					<-r.Context().Done()
					return
				}
				switch current {
				case "release":
					json.NewEncoder(w).Encode(map[string]any{"tag_name": "new", "assets": []map[string]string{{"name": "binary", "browser_download_url": serverURL + "/asset"}}})
				case "location":
					w.Header().Set("Location", serverURL+"/download")
					w.WriteHeader(http.StatusFound)
				case "range":
					w.Header().Set("ETag", "candidate")
					w.WriteHeader(http.StatusPartialContent)
				}
			}))
			defer srv.Close()
			serverURL = srv.URL
			f := &Github{User: "user", Repo: "repo", Asset: func(string) bool { return true }}
			f.Init()
			f.releaseURL = srv.URL + "/release"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				r, err := f.FetchContext(ctx)
				if r != nil {
					r.(io.Closer).Close()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request stage not reached")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("request not canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request cancellation stuck")
			}
		})
	}
}

func TestS3ContextCancelsMetadataBody(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY"} {
		t.Setenv(name, "")
	}
	entered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (&S3{}).credentialsContext(ctx, &http.Client{Timeout: time.Minute}, srv.URL+"/")
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("metadata body not canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("metadata cancellation stuck")
	}
}

func TestFileContextCancelsStabilityWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(path, []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &File{Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := f.FetchContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("file wait not canceled", err)
	}
}
