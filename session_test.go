package xiaomidrive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r}
}

func testClient(t *testing.T, fn testTransport) *Client {
	t.Helper()
	c := NewClient(WithGalleryReadInterval(0))
	if err := c.s.setCredentials(Credentials{UserID: "test-user", ServiceToken: "test-token"}); err != nil {
		t.Fatal(err)
	}
	c.s.http = &http.Client{Transport: fn}
	c.s.noRedir = &http.Client{Transport: fn}
	return c
}

func TestSessionRejectsHTTPAndMalformedSuccess(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{502, "<html>bad gateway</html>"}, {503, ""}, {500, `{"code":0,"data":null}`},
		{200, ""}, {200, "null"}, {200, `{}`}, {200, `{"code":null}`}, {200, "<html>error</html>"},
	} {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.body), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				resp := testResponse(r, tc.status, tc.body)
				resp.Header.Add("Set-Cookie", "serviceToken=untrusted; Path=/")
				return resp, nil
			})
			_, err := c.s.get(context.Background(), "/test", nil)
			if err == nil || calls != 1 {
				t.Fatalf("error=%v, calls=%d; expected error without retrying an HTTP response", err, calls)
			}
			if c.s.serviceToken() != "test-token" {
				t.Fatal("failed response changed credentials")
			}
			if tc.status != 200 {
				var api *APIError
				if !errors.As(err, &api) || api.HTTPStatus != tc.status {
					t.Fatalf("HTTP status lost: %v", err)
				}
			}
		})
	}
}

func TestSession401RefreshWithNonJSONBody(t *testing.T) {
	for _, body := range []string{"", "<html>expired</html>", `{"R":401}`} {
		t.Run(body, func(t *testing.T) {
			business := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/test":
					business++
					if business == 1 {
						return testResponse(r, 401, body), nil
					}
					if !strings.Contains(r.Header.Get("Cookie"), "serviceToken=fresh") {
						t.Error("retry did not use refreshed service token")
					}
					return testResponse(r, 200, `{"code":0,"data":{"ok":true}}`), nil
				case "/api/user/login":
					return testResponse(r, 200, `{"data":{"loginUrl":"https://account.test/login?sid=micloud"}}`), nil
				case "/login":
					return testResponse(r, 200, `{"code":0,"securityStatus":0,"location":"https://account.test/sts"}`), nil
				case "/sts":
					resp := testResponse(r, 200, "")
					resp.Header.Add("Set-Cookie", "serviceToken=fresh; Path=/")
					return resp, nil
				default:
					t.Fatalf("unexpected request %s", r.URL.Path)
					return nil, nil
				}
			})
			c.s.creds.PassToken = "restore-token"
			if _, err := c.s.get(context.Background(), "/test", nil); err != nil || business != 2 {
				t.Fatalf("error=%v business calls=%d", err, business)
			}
		})
	}
}

func TestFileStoreConcurrentAtomicSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Different FileStore instances also share this target path.
			s := &FileStore{Path: path}
			if err := s.Save(&Credentials{UserID: fmt.Sprint(i), ServiceToken: strings.Repeat("x", i*1000)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if _, err := (&FileStore{Path: path}).Load(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("permissions: %v %v", fi, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

type orderedStore struct {
	mu      sync.Mutex
	c       Credentials
	entered chan struct{}
	release chan struct{}
}

func (s *orderedStore) Load() (*Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.c
	return &c, nil
}

func (s *orderedStore) Save(c *Credentials) error {
	if c.ServiceToken == "first" {
		close(s.entered)
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c = *c
	return nil
}

func TestSessionPersistsUpdatesInOrder(t *testing.T) {
	store := &orderedStore{entered: make(chan struct{}), release: make(chan struct{})}
	s := newSession(WithStore(store))
	first, second := make(chan struct{}), make(chan struct{})
	go func() { s.updateServiceToken("first", "user"); close(first) }()
	<-store.entered
	go func() { s.updateServiceToken("second", "user"); close(second) }()
	select {
	case <-second:
		t.Error("newer snapshot saved while older save was still in progress")
	case <-time.After(20 * time.Millisecond):
	}
	close(store.release)
	<-first
	<-second
	saved, _ := store.Load()
	if saved.ServiceToken != "second" || s.Credentials() != *saved {
		t.Fatal("persisted credentials do not match final in-memory credentials")
	}
}

type failingStore struct{ Credentials }

func (s *failingStore) Load() (*Credentials, error) { c := s.Credentials; return &c, nil }
func (*failingStore) Save(*Credentials) error       { return errors.New("disk full") }

func TestLoginValidationPreservesCredentials(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		return testResponse(r, 200, `{"code":12345}`), nil
	})
	old := c.s.Credentials()
	if err := c.LoginWithCookies(context.Background(), Credentials{UserID: "bad", ServiceToken: "bad"}); err == nil {
		t.Fatal("invalid cookies accepted")
	}
	saved, _ := c.s.store.Load()
	if c.s.Credentials() != old || *saved != old {
		t.Fatal("failed login replaced valid credentials")
	}
	c.s.http.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		return testResponse(r, 200, `{"code":0,"data":{}}`), nil
	})
	c.s.store = &failingStore{Credentials: old}
	if err := c.LoginWithCookies(context.Background(), Credentials{UserID: "new", ServiceToken: "valid"}); err == nil {
		t.Fatal("persistence failure was hidden")
	}
	if c.s.Credentials() != old {
		t.Fatal("save failure changed active credentials")
	}
	c.s.store = &memStore{c: old}
	if err := c.LoginWithCookies(context.Background(), Credentials{UserID: "new", ServiceToken: "valid"}); err != nil {
		t.Fatal(err)
	}
	saved, _ = c.s.store.Load()
	if saved.UserID != "new" || c.s.Credentials() != *saved {
		t.Fatal("validated login was not committed")
	}
}

func TestFinishLoginRequiresUserID(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		resp := testResponse(r, 200, "")
		resp.Header.Add("Set-Cookie", "serviceToken=new; Path=/")
		return resp, nil
	})
	old := c.s.Credentials()
	if err := c.finishLogin(context.Background(), "", "", "", "https://account.test/sts", "", ""); err == nil {
		t.Fatal("missing user ID accepted")
	}
	if c.s.Credentials() != old {
		t.Fatal("incomplete login changed credentials")
	}
}
