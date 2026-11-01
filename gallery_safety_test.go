package xiaomidrive

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGalleryE2EEFailuresAreRetriedAndNeverPrepared(t *testing.T) {
	for _, body := range []string{
		`{"code":12345}`, `{"code":0,"data":[]}`, `{"code":0,"data":{}}`,
		`{"code":0,"data":{"e2eeStatus":"open"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			checks, prepares := 0, 0
			valid := false
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "getEncInfo") {
					checks++
					if valid {
						return testResponse(r, 200, `{"code":0,"data":{"e2eeStatus":"close"}}`), nil
					}
					return testResponse(r, 200, body), nil
				}
				prepares++
				return testResponse(r, 200, `{"code":0,"data":{}}`), nil
			})
			g := c.Gallery()
			if err := g.ensure(context.Background()); err == nil || g.ready || prepares != 0 {
				t.Fatalf("error=%v ready=%v prepares=%d", err, g.ready, prepares)
			}
			valid = true
			if err := g.ensure(context.Background()); err != nil || !g.ready || checks != 2 || prepares != 1 {
				t.Fatalf("failed to retry initialization: %v, checks=%d prepares=%d", err, checks, prepares)
			}
			if err := g.ensure(context.Background()); err != nil || checks != 2 {
				t.Fatalf("successful initialization not cached: %v", err)
			}
			// A different account must not inherit the previous account's check.
			if err := c.s.setCredentials(Credentials{UserID: "other-user", ServiceToken: "token"}); err != nil {
				t.Fatal(err)
			}
			if err := g.ensure(context.Background()); err != nil || checks != 3 {
				t.Fatalf("new account inherited ready state: %v", err)
			}
		})
	}
}

func TestGalleryConcurrentReadsShareSpacing(t *testing.T) {
	var mu sync.Mutex
	var starts []time.Time
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return testResponse(r, 200, `{"code":0,"data":{}}`), nil
	})
	c.s.galleryReadInterval = 20 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Gallery().get(context.Background(), "/gallery/user/test", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(starts) != 3 {
		t.Fatalf("requests=%d", len(starts))
	}
	for i := 1; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 18*time.Millisecond {
			t.Fatalf("concurrent requests bypassed spacing: %v", gap)
		}
	}
}

func TestGalleryWaitingForRequestGateIsCancelable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return testResponse(r, 200, `{"code":0,"data":{}}`), nil
	})
	finished := make(chan struct{})
	go func() { defer close(finished); c.Gallery().get(context.Background(), "/gallery/user/test", nil) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Gallery().get(ctx, "/gallery/user/test", nil)
	close(release)
	<-finished
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error=%v", err)
	}
}
