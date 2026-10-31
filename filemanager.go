package micloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

type operateRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	ParentID string `json:"parentId,omitempty"`
}

func recordType(f *File) string {
	if f.Type != "" {
		return string(f.Type)
	}
	if f.IsDir() {
		return string(TypeFolder)
	}
	return string(TypeOther)
}

// Move relocates items into dstParentID. On a name clash the moved item is
// auto-renamed by the service (no error).
func (c *Client) Move(ctx context.Context, items []*File, dstParentID string) error {
	recs := make([]operateRecord, 0, len(items))
	for _, f := range items {
		recs = append(recs, operateRecord{ID: f.ID, Type: recordType(f), ParentID: dstParentID})
	}
	return c.fileManager(ctx, "MOVE", recs)
}

// Copy server-side copies items into dstParentID. A name clash in the target
// yields a TaskError wrapping ErrExist (the service does not auto-rename for
// copy); copying into the item's own folder yields ErrSameFolder.
func (c *Client) Copy(ctx context.Context, items []*File, dstParentID string) error {
	recs := make([]operateRecord, 0, len(items))
	for _, f := range items {
		recs = append(recs, operateRecord{ID: f.ID, Type: recordType(f), ParentID: dstParentID})
	}
	return c.fileManager(ctx, "COPY", recs)
}

// Remove deletes items (into the recycle bin). Deleting a folder removes its
// contents too.
func (c *Client) Remove(ctx context.Context, items []*File) error {
	recs := make([]operateRecord, 0, len(items))
	for _, f := range items {
		recs = append(recs, operateRecord{ID: f.ID, Type: recordType(f)})
	}
	return c.fileManager(ctx, "DELETE", recs)
}

func (c *Client) fileManager(ctx context.Context, op string, recs []operateRecord) error {
	if len(recs) == 0 {
		return nil
	}
	payload, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	form := url.Values{"operateType": {op}, "operateRecords": {string(payload)}}
	raw, err := c.s.postForm(ctx, "/drive/v2/user/records/filemanager", form)
	if err != nil {
		return err
	}
	var d struct {
		TaskID   string  `json:"taskId"`
		Interval flexInt `json:"interval"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("micloud: %s decode: %w", op, err)
	}
	interval := time.Duration(d.Interval) * time.Millisecond
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	return c.waitTask(ctx, op, d.TaskID, interval)
}

type taskResult struct {
	Status        string `json:"status"`
	ErrCode       int    `json:"errCode"`
	ErrMessage    string `json:"errMessage"`
	FailedRecords []struct {
		ID         flexString `json:"id"`
		ErrCode    int        `json:"errCode"`
		Code       int        `json:"code"`
		Message    string     `json:"message"`
		ErrMessage string     `json:"errMessage"`
		// Observed shape: the source record is under "from" (and the
		// conflicting target under "to"); there is no top-level id.
		From struct {
			ID   flexString `json:"id"`
			Name string     `json:"name"`
		} `json:"from"`
	} `json:"failedRecords"`
}

func (c *Client) waitTask(ctx context.Context, op, taskID string, interval time.Duration) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		q := url.Values{"taskId": {taskID}}
		raw, err := c.s.get(ctx, "/drive/v2/user/records/taskquery", q)
		if err != nil {
			return err
		}
		var t taskResult
		if err := json.Unmarshal(raw, &t); err != nil {
			return fmt.Errorf("micloud: %s task decode: %w", op, err)
		}
		switch t.Status {
		case "SUCCESS":
			if len(t.FailedRecords) == 0 {
				return nil
			}
			return taskErrorFrom(op, taskID, &t)
		case "FAILED":
			return taskErrorFrom(op, taskID, &t)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("micloud: %s task %s timed out", op, taskID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func taskErrorFrom(op, taskID string, t *taskResult) error {
	te := &TaskError{Op: op, TaskID: taskID, Code: t.ErrCode, Message: t.ErrMessage}
	for _, fr := range t.FailedRecords {
		code := fr.ErrCode
		if code == 0 {
			code = fr.Code
		}
		msg := fr.ErrMessage
		if msg == "" {
			msg = fr.Message
		}
		id := string(fr.ID)
		if id == "" {
			id = string(fr.From.ID)
		}
		te.Failed = append(te.Failed, FailedRecord{ID: id, Name: fr.From.Name, Code: code, Message: msg})
	}
	return te
}
