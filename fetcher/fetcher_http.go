package fetcher

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

//HTTPFetcher uses HEAD requests to poll the status of a given
//file. If it detects this file has been updated, it will fetch
//and stream out to the binary writer.
type HTTP struct {
	//URL to poll for new binaries
	URL          string
	Interval     time.Duration
	CheckHeaders []string
	//interal state
	delay bool
	lasts map[string]string
}

//if any of these change, the binary has been updated
var defaultHTTPCheckHeaders = []string{"ETag", "If-Modified-Since", "Last-Modified", "Content-Length"}

func (h *HTTP) Fetch() (io.Reader, error) {
	//apply defaults
	if h.URL == "" {
		return nil, fmt.Errorf("fetcher.HTTP requires a URL")
	}
	if h.Interval == 0 {
		h.Interval = 5 * time.Minute
	}
	if h.CheckHeaders == nil {
		h.CheckHeaders = defaultHTTPCheckHeaders
	}
	if h.lasts == nil {
		h.lasts = map[string]string{}
	}
	//delay fetches after first
	if h.delay {
		time.Sleep(h.Interval)
	}
	h.delay = true
	//status check using HEAD
	resp, err := http.Head(h.URL)
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
	resp, err = http.Get(h.URL)
	if err != nil {
		return nil, fmt.Errorf("GET request failed (%s)", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET request failed (status code %d)", resp.StatusCode)
	}
	//success!
	return resp.Body, nil
}
