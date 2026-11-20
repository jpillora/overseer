package fetcher

import (
	"github.com/jpillora/s3"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestS3MetadataDeadlineIncludesBody(t *testing.T) {
	for _, n := range []string{"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY"} {
		t.Setenv(n, "")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	start := time.Now()
	_, err := (&S3{}).credentials(&http.Client{Timeout: 100 * time.Millisecond}, srv.URL+"/")
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("IAM lookup did not honor deadline", err)
	}
}

func TestS3CredentialsPrecedenceAndSessionToken(t *testing.T) {
	for _, n := range []string{"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY", "AWS_SESSION_TOKEN", "AWS_SECURITY_TOKEN"} {
		t.Setenv(n, "")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "env-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	t.Setenv("AWS_SESSION_TOKEN", "env-token")
	check := func(f *S3, access, token string) {
		t.Helper()
		opt, err := f.credentials(&http.Client{Timeout: time.Second}, "http://invalid.example/")
		if err != nil {
			t.Fatal(err)
		}
		req, err := s3.NewRequest("GET", opt, s3.Bucket("test"), s3.Key("binary"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.Header.Get("Authorization"), "Credential="+access+"/") || req.Header.Get("X-Amz-Security-Token") != token {
			t.Fatal("wrong credential source")
		}
	}
	check(&S3{}, "env-access", "env-token")
	check(&S3{Access: "explicit-access", Secret: "explicit-secret"}, "explicit-access", "")
}

func TestS3MetadataResponses(t *testing.T) {
	for _, n := range []string{"AWS_ACCESS_KEY_ID", "AWS_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY", "AWS_SECRET_KEY"} {
		t.Setenv(n, "")
	}
	for _, kind := range []string{"valid", "status", "oversized", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "status" {
					w.WriteHeader(503)
					return
				}
				if kind == "oversized" {
					w.Write([]byte(strings.Repeat("x", 64*1024+1)))
					return
				}
				if r.URL.Path == "/" {
					w.Write([]byte("role-name\n"))
					return
				}
				if kind == "invalid" {
					w.Write([]byte(`{}`))
					return
				}
				w.Write([]byte(`{"AccessKeyID":"role-access","SecretAccessKey":"role-secret","Token":"role-token","Expiration":"2099-01-01T00:00:00Z"}`))
			}))
			defer srv.Close()
			opt, err := (&S3{}).credentials(&http.Client{Timeout: time.Second}, srv.URL+"/")
			if kind != "valid" {
				if err == nil {
					t.Fatal("invalid metadata accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			req, err := s3.NewRequest("GET", opt, s3.Bucket("test"), s3.Key("binary"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(req.Header.Get("Authorization"), "Credential=role-access/") || req.Header.Get("X-Amz-Security-Token") != "role-token" {
				t.Fatal("metadata credentials not used")
			}
		})
	}
}
