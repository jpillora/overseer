package fetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTP fetcher uses HEAD requests to poll the status of a given
// file. If it detects this file has been updated, it will fetch
// and return its io.Reader stream.
type HTTP struct {
	//URL to poll for new binaries
	URL          string
	Interval     time.Duration
	CheckHeaders []string
	// Timeout bounds each complete request, including the body. Defaults to five minutes.
	Timeout time.Duration
	//internal state
	delay    bool
	lasts    map[string]string
	previous map[string]string
}

// if any of these change, the binary has been updated
var defaultHTTPCheckHeaders = []string{"ETag", "If-Modified-Since", "Last-Modified", "Content-Length"}

// Init validates the provided config
func (h *HTTP) Init() error {
	//apply defaults
	if h.URL == "" {
		return fmt.Errorf("URL required")
	}
	h.lasts = map[string]string{}
	if h.Timeout <= 0 {
		h.Timeout = 5 * time.Minute
	}
	if h.Interval == 0 {
		h.Interval = 5 * time.Minute
	}
	if h.CheckHeaders == nil {
		h.CheckHeaders = defaultHTTPCheckHeaders
	}
	return nil
}

// Fetch the binary from the provided URL
func (h *HTTP) Fetch() (io.Reader, error) {
	return h.FetchContext(context.Background())
}

// FetchContext is Fetch with cancelable polling and HTTP requests.
func (h *HTTP) FetchContext(ctx context.Context) (io.Reader, error) {
	h.previous = make(map[string]string, len(h.lasts))
	for k, v := range h.lasts {
		h.previous[k] = v
	}
	//delay fetches after first
	if h.delay {
		if err := wait(ctx, h.Interval); err != nil {
			return nil, err
		}
	}
	h.delay = true
	client := &http.Client{Timeout: h.Timeout}
	//status check using HEAD
	req, err := http.NewRequestWithContext(ctx, "HEAD", h.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HEAD request failed (%w)", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HEAD request failed (status code %d)", resp.StatusCode)
	}
	//if all headers match, skip update
	matches, total := 0, 0
	for _, header := range h.CheckHeaders {
		if curr := resp.Header.Get(header); curr != "" {
			if last, ok := h.lasts[header]; ok && last == curr {
				matches++
			}
			h.lasts[header] = curr
			total++
		}
	}
	if total > 0 && matches == total {
		return nil, nil //skip, file match
	}
	//binary fetch using GET
	req, err = http.NewRequestWithContext(ctx, "GET", h.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err = client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET request failed (%w)", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET request failed (status code %d)", resp.StatusCode)
	}
	return responseReader(resp, strings.HasSuffix(h.URL, ".gz"))
}

func (h *HTTP) FetchResult(err error) {
	if err != nil && h.previous != nil {
		h.lasts = h.previous
	}
	h.previous = nil
}

func (h *HTTP) Reset() { h.lasts = map[string]string{} }
