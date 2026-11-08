package fetcher

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jpillora/s3"
)

func (s *S3) credentials(client *http.Client, metadataURL string) (s3.Option, error) {
	if s.Access != "" && s.Secret != "" {
		return s3.Credentials(s.Access, s.Secret), nil
	}
	first := func(names ...string) string {
		for _, n := range names {
			if v := os.Getenv(n); v != "" {
				return v
			}
		}
		return ""
	}
	access := first("AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY")
	secret := first("AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY")
	if access != "" && secret != "" {
		return s3.STSCredentials(access, secret, first("AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN"), time.Time{}), nil
	}
	read := func(address string) ([]byte, error) {
		resp, err := client.Get(address)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("IAM metadata status %s", resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
		if err != nil {
			return nil, err
		}
		if len(b) > 64*1024 {
			return nil, fmt.Errorf("IAM metadata response too large")
		}
		return b, nil
	}
	roles, err := read(metadataURL)
	if err != nil {
		return nil, fmt.Errorf("S3 credentials: %w", err)
	}
	names := strings.Fields(string(roles))
	if len(names) == 0 {
		return nil, fmt.Errorf("no IAM role credentials available")
	}
	b, err := read(metadataURL + url.PathEscape(names[0]))
	if err != nil {
		return nil, err
	}
	var creds struct {
		AccessKeyID, SecretAccessKey, Token string
		Expiration                          time.Time
	}
	if err := json.Unmarshal(b, &creds); err != nil {
		return nil, err
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return nil, fmt.Errorf("IAM metadata returned no credentials")
	}
	return s3.STSCredentials(creds.AccessKeyID, creds.SecretAccessKey, creds.Token, creds.Expiration), nil
}
