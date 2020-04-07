package fetcher

import (
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jpillora/s3"
)

//S3 uses authenticated HEAD requests to poll the status of a given
//object. If it detects this file has been updated, it will perform
//an object GET and return its io.Reader stream.
type S3 struct {
	//Access key falls back to env AWS_ACCESS_KEY, then metadata
	Access string
	//Secret key falls back to env AWS_SECRET_ACCESS_KEY, then metadata
	Secret string
	//Region defaults to ap-southeast-2
	Region string
	Bucket string
	Key    string
	//Interval between checks
	Interval time.Duration
	//HeadTimeout defaults to 5 seconds
	HeadTimeout time.Duration
	//GetTimeout defaults to 5 minutes
	GetTimeout time.Duration
	//interal state
	client   *http.Client
	delay    bool
	lastETag string
}

// Init validates the provided config
func (s *S3) Init() error {
	if s.Bucket == "" {
		return errors.New("S3 bucket not set")
	} else if s.Key == "" {
		return errors.New("S3 key not set")
	}
	if s.Region == "" {
		s.Region = "ap-southeast-2"
	}
	//initial etag
	if p, _ := os.Executable(); p != "" {
		if f, err := os.Open(p); err == nil {
			h := md5.New()
			io.Copy(h, f)
			f.Close()
			s.lastETag = hex.EncodeToString(h.Sum(nil))
		}
	}
	//apply defaults
	if s.Interval <= 0 {
		s.Interval = 5 * time.Minute
	}
	if s.HeadTimeout <= 0 {
		s.HeadTimeout = 5 * time.Second
	}
	if s.GetTimeout <= 0 {
		s.GetTimeout = 5 * time.Minute
	}
	return nil
}

// Fetch the binary from S3
func (s *S3) Fetch() (io.Reader, error) {
	//delay fetches after first
	if s.delay {
		time.Sleep(s.Interval)
	}
	s.delay = true
	//http client where we change the timeout
	c := http.Client{}
	//options for this key
	creds := s3.AmbientCredentials()
	if s.Access != "" && s.Secret != "" {
		creds = s3.Credentials(s.Access, s.Secret)
	}
	opts := []s3.Option{creds, s3.Region(s.Region), s3.Bucket(s.Bucket), s3.Key(s.Key)}
	//status check using HEAD
	req, err := s3.NewRequest("HEAD", opts...)
	if err != nil {
		return nil, err
	}
	c.Timeout = s.HeadTimeout
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HEAD request failed (%s)", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HEAD request failed (%s)", resp.Status)
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if s.lastETag == etag {
		return nil, nil //skip, file match
	}
	s.lastETag = etag
	//binary fetch using GET
	req, err = s3.NewRequest("GET", opts...)
	if err != nil {
		return nil, err
	}
	c.Timeout = s.GetTimeout
	resp, err = c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET request failed (%s)", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET request failed (%s)", resp.Status)
	}
	//extract gz files
	if strings.HasSuffix(s.Key, ".gz") && resp.Header.Get("Content-Encoding") != "gzip" {
		return gzip.NewReader(resp.Body)
	}
	//success!
	return resp.Body, nil
}
