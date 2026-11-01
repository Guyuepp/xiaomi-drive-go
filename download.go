package xiaomidrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	rangeHeader, err := downloadRange(offset, length)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	l, err := c.Link(ctx, id)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := c.s.http.Do(req)
	if err != nil {
		return nil, err
	}
	return downloadResponse(resp, offset, length, "download "+id)
}

// Open reads an entire file from the start.
func (c *Client) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	return c.Download(ctx, id, 0, -1)
}
