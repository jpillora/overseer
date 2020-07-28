package fetcher

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

//HTTP fetcher uses HEAD requests to poll the status of a given
//file. If it detects this file has been updated, it will fetch
//and return its io.Reader stream.
type HTTP struct {
	//URL to poll for new binaries
	URL          string
	Interval     time.Duration
	CheckHeaders []string
	//internal state
	delay   bool
	lasts   map[string]string
	URLFunc func() (URL string, err error)
}

//if any of these change, the binary has been updated
var defaultHTTPCheckHeaders = []string{"ETag", "If-Modified-Since", "Last-Modified", "Content-Length"}

// Init validates the provided config
func (h *HTTP) Init() error {
	//apply defaults
	if h.URL == "" && h.URLFunc == nil {
		return fmt.Errorf("URL or URLFunc required")
	}
	h.lasts = map[string]string{}
	if h.Interval == 0 {
		h.Interval = 5 * time.Minute
	}
	if h.CheckHeaders == nil {
		h.CheckHeaders = defaultHTTPCheckHeaders
	}
	return nil
}

// Fetch the binary from the provided URL
func (h *HTTP) Fetch() (r io.Reader, err error) {
	//delay fetches after first
	if h.delay {
		time.Sleep(h.Interval)
	}
	h.delay = true

	URL := h.URL

	if URL == "" {
		if URL, err = h.URLFunc(); err != nil {
			return
		}
	}

	//status check using HEAD
	resp, err := http.Head(URL)
	if err != nil {
		return nil, fmt.Errorf("HEAD request failed (%s)", err)
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
	if matches == total {
		return nil, nil //skip, file match
	}
	//binary fetch using GET
	resp, err = http.Get(URL)
	if err != nil {
		return nil, fmt.Errorf("GET request failed (%s)", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET request failed (status code %d)", resp.StatusCode)
	}
	//extract gz files
	if strings.HasSuffix(URL, ".gz") && resp.Header.Get("Content-Encoding") != "gzip" {
		return gzip.NewReader(resp.Body)
	}
	//success!
	return resp.Body, nil
}
