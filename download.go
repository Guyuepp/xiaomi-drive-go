package xiaomidrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type storageInfo struct {
	DownloadURL string  `json:"downloadUrl"`
	ExpireAt    flexInt `json:"expireAt"`
}

// fileWithStorage decodes a files/<id> detail: the record fields plus storage.
type fileWithStorage struct {
	rawRecord
	Storage storageInfo `json:"storage"`
}

func (c *Client) detail(ctx context.Context, id string) (*File, storageInfo, error) {
	raw, err := c.s.get(ctx, "/drive/v2/user/files/"+id, nil)
	if err != nil {
		return nil, storageInfo{}, err
	}
	var d fileWithStorage
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, storageInfo{}, fmt.Errorf("xiaomidrive: detail decode: %w", err)
	}
	if d.ID == "" {
		d.ID = flexString(id)
	}
	return d.toFile(nil), d.Storage, nil
}

// Link returns a direct, cookie-free download URL for a file. It supports Range
// requests and is valid for about 24 hours.
func (c *Client) Link(ctx context.Context, id string) (*Link, error) {
	_, st, err := c.detail(ctx, id)
	if err != nil {
		return nil, err
	}
	if st.DownloadURL == "" {
		return nil, fmt.Errorf("%w: no download url for %s", ErrNotFound, id)
	}
	l := &Link{URL: st.DownloadURL}
	if st.ExpireAt > 0 {
		l.Expires = time.UnixMilli(int64(st.ExpireAt))
	}
	return l, nil
}

// Download opens a file for reading. offset is the starting byte; length < 0
// reads to the end. The caller must close the returned reader.
func (c *Client) Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error) {
	l, err := c.Link(ctx, id)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	if offset > 0 || length >= 0 {
		if length >= 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
	}
	resp, err := c.s.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("xiaomidrive: download %s: HTTP %d", id, resp.StatusCode)
	}
	return resp.Body, nil
}

// Open reads an entire file from the start.
func (c *Client) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	return c.Download(ctx, id, 0, -1)
}
