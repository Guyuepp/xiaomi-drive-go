package xiaomidrive

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

type closeTracker struct {
	io.Reader
	closed bool
}

func (r *closeTracker) Close() error { r.closed = true; return nil }

func TestDownloadResponses(t *testing.T) {
	for _, tc := range []struct {
		name           string
		offset, length int64
		status         int
		contentRange   string
		body, want     string
		wantError      bool
	}{
		{"full", 0, -1, 200, "", "abcdef", "abcdef", false},
		{"limited full", 0, 2, 200, "", "abcdef", "ab", false},
		{"ignored offset", 3, 2, 200, "", "abcdef", "", true},
		{"partial", 3, 2, 206, "bytes 3-4/6", "de", "de", false},
		{"partial to end", 3, -1, 206, "bytes 3-5/6", "def", "def", false},
		{"end of file", 3, 20, 206, "bytes 3-5/6", "def", "def", false},
		{"incomplete range", 3, 3, 206, "bytes 3-4/6", "de", "", true},
		{"incomplete open", 0, -1, 206, "bytes 0-1/6", "ab", "", true},
		{"wrong start", 3, 2, 206, "bytes 0-1/6", "ab", "", true},
		{"missing range", 3, 2, 206, "", "de", "", true},
		{"wrong total", 3, 2, 206, "bytes 3-4/4", "de", "", true},
		{"too many bytes", 3, 2, 206, "bytes 3-5/6", "def", "", true},
		{"overflow span", 0, -1, 206, "bytes 0-9223372036854775807/*", "x", "", true},
		{"wrong content length", 3, 2, 206, "bytes 3-4/6", "def", "", true},
		{"storage error", 0, -1, 503, "", "failed", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := testResponse(nil, tc.status, tc.body)
			resp.Header.Set("Content-Range", tc.contentRange)
			body := &closeTracker{Reader: strings.NewReader(tc.body)}
			resp.Body = body
			rc, err := downloadResponse(resp, tc.offset, tc.length, "test")
			if tc.wantError {
				if err == nil || !body.closed {
					t.Fatalf("error=%v body closed=%v", err, body.closed)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil || string(b) != tc.want || !body.closed {
				t.Fatalf("body=%q error=%v closed=%v", b, err, body.closed)
			}
		})
	}
}

func TestDownloadValidatesBeforeRequest(t *testing.T) {
	c := testClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected HTTP request")
		return nil, nil
	})
	for _, download := range []func(context.Context, string, int64, int64) (io.ReadCloser, error){c.Download, c.Gallery().Download} {
		for _, bounds := range [][2]int64{{-1, 2}, {math.MaxInt64, 2}} {
			if _, err := download(context.Background(), "file", bounds[0], bounds[1]); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("invalid range accepted: %v", err)
			}
		}
		rc, err := download(context.Background(), "file", 3, 0)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if len(b) != 0 {
			t.Fatal("zero length read returned data")
		}
	}
}

func TestDownloadDetectsTruncatedBodies(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		contentLength int64
	}{
		{"chunked partial", http.StatusPartialContent, -1},
		{"sized partial", http.StatusPartialContent, 2},
		{"sized full", http.StatusOK, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := testResponse(nil, tc.status, "a")
			resp.ContentLength = tc.contentLength
			resp.Header.Set("Content-Range", "bytes 0-1/2")
			rc, err := downloadResponse(resp, 0, 2, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			if _, err := io.ReadAll(rc); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("short response error=%v, want unexpected EOF", err)
			}
		})
	}
	// An unsized 200 response may legitimately reach EOF before the requested
	// length when the entire file is shorter than that range.
	resp := testResponse(nil, http.StatusOK, "a")
	resp.ContentLength = -1
	rc, err := downloadResponse(resp, 0, 2, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if body, err := io.ReadAll(rc); err != nil || string(body) != "a" {
		t.Fatalf("unsized full response: body=%q error=%v", body, err)
	}
}

func TestDownloadRangeHeadersAndIgnoredRange(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "storage.test" {
			if r.Header.Get("Range") != "bytes=3-4" {
				t.Errorf("Range=%q", r.Header.Get("Range"))
			}
			return testResponse(r, 200, "abcdef"), nil
		}
		return testResponse(r, 200, `{"code":0,"data":{"id":"file","storage":{"downloadUrl":"https://storage.test/file"}}}`), nil
	})
	if _, err := c.Download(context.Background(), "file", 3, 2); err == nil {
		t.Fatal("ignored offset accepted")
	}
}
