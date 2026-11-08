package fetcher

import (
	"compress/gzip"
	"io"
	"net/http"
)

// gzip.Reader.Close does not close its source. Keep ownership of the HTTP body
// even for corrupt headers, rejected candidates and partially read streams.
func responseReader(resp *http.Response, compressed bool) (io.Reader, error) {
	if !compressed || resp.Header.Get("Content-Encoding") == "gzip" {
		return resp.Body, nil
	}
	z, err := gzip.NewReader(resp.Body)
	if err != nil {
		resp.Body.Close()
		return nil, err
	}
	return &gzipBody{Reader: z, body: resp.Body}, nil
}

type gzipBody struct {
	*gzip.Reader
	body io.Closer
}

func (b *gzipBody) Close() error { b.Reader.Close(); return b.body.Close() }
