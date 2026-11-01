package xiaomidrive

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMakeDirMissingType(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		return testResponse(r, 200, `{"code":0,"data":{"id":"folder","name":"folder"}}`), nil
	})
	f, err := c.MakeDir(context.Background(), RootID, "folder")
	if err != nil || !f.IsDir() {
		t.Fatalf("folder=%v error=%v", f, err)
	}
}

func TestOverwriteRejectsInvalidUploadBeforeRemovingOld(t *testing.T) {
	for _, id := range []string{"", RootID} {
		t.Run("commit-"+id, func(t *testing.T) {
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				body := `{}`
				switch {
				case strings.HasSuffix(r.URL.Path, "/children"):
					body = `{"records":[{"id":"old","name":"target","type":"other"}],"hasMore":false}`
				case strings.HasSuffix(r.URL.Path, "/create"):
					body = `{"uploadId":"up","kss":{"node_urls":["https://storage.test"],"block_metas":[{"is_existed":1,"commit_meta":"cm"}]}}`
				case strings.HasSuffix(r.URL.Path, "/commit"):
					body = `{"id":"` + id + `"}`
				default:
					t.Fatalf("invalid upload triggered subsequent request %s", r.URL.Path)
				}
				return testResponse(r, 200, `{"code":0,"data":`+body+`}`), nil
			})
			if _, err := c.Overwrite(context.Background(), RootID, "target", strings.NewReader("x"), UploadOptions{}); err == nil {
				t.Fatal("invalid upload result accepted")
			}
		})
	}
}

func TestTaskWaitDeadlineIncludesIDAndCanResume(t *testing.T) {
	complete := false
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		if complete {
			return testResponse(r, 200, `{"code":0,"data":{"status":"SUCCESS"}}`), nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	c.s.taskTimeout = 10 * time.Millisecond
	err := c.WaitTask(context.Background(), "task-id")
	var wait *TaskWaitError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &wait) || wait.TaskID != "task-id" {
		t.Fatalf("task identity or timeout lost: %v", err)
	}
	complete = true
	if err := c.WaitTask(context.Background(), wait.TaskID); err != nil {
		t.Fatal(err)
	}
}
