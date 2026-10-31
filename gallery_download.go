package xiaomidrive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// galleryJSONPCallback is the callBack name sent to /gallery/storage; the
// storage redirect wraps its JSON in a call to it.
const galleryJSONPCallback = "dl"

// galleryTarget is where the original bytes of a photo can be fetched: POST
// URL with form meta=Meta. Neither step needs i.mi.com cookies.
type galleryTarget struct {
	URL  string `json:"url"`
	Meta string `json:"meta"`
}

// resolveDownload performs the two lookup steps of gallery-notes §5.1. Images
// and videos use the same path. How long a target stays valid is not known
// yet, so it is fetched again for every Download.
func (g *Gallery) resolveDownload(ctx context.Context, id string) (*galleryTarget, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	q := url.Values{"id": {id}, "callBack": {galleryJSONPCallback}}
	raw, err := g.get(ctx, "/gallery/storage", q)
	if err != nil {
		return nil, err
	}
	var d struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery storage decode: %w", err)
	}
	if d.URL == "" {
		return nil, fmt.Errorf("%w: no download url for photo %s", ErrNotFound, id)
	}

	resp, body, err := httpGetBody(ctx, g.c.s.http, d.URL, "", nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xiaomidrive: gallery download lookup: HTTP %d", resp.StatusCode)
	}
	payload, err := stripJSONP(body)
	if err != nil {
		return nil, err
	}
	var t galleryTarget
	if err := json.Unmarshal(payload, &t); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery download lookup decode: %w", err)
	}
	if t.URL == "" || t.Meta == "" {
		return nil, fmt.Errorf("xiaomidrive: gallery download lookup: incomplete response")
	}
	return &t, nil
}

// stripJSONP turns `cb({...})` (optionally with a trailing `;`) into `{...}`.
func stripJSONP(b []byte) ([]byte, error) {
	b = bytes.TrimSpace(b)
	b = bytes.TrimSuffix(b, []byte(";"))
	open := bytes.IndexByte(b, '(')
	if open < 0 || len(b) == 0 || b[len(b)-1] != ')' {
		return nil, fmt.Errorf("xiaomidrive: gallery download lookup: not a JSONP response")
	}
	return b[open+1 : len(b)-1], nil
}

// Download opens a photo or video for reading. offset is the starting byte;
// length < 0 reads to the end. The bytes are the original upload; their SHA-1
// matches Photo.SHA1. The caller must close the returned reader.
func (g *Gallery) Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error) {
	t, err := g.resolveDownload(ctx, id)
	if err != nil {
		return nil, err
	}
	// The storage node only answers a POST carrying meta; a GET is rejected.
	form := url.Values{"meta": {t.Meta}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ranged := offset > 0 || length >= 0
	if ranged {
		if length >= 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
	}
	resp, err := g.c.s.http.Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		return resp.Body, nil
	case resp.StatusCode == http.StatusOK && offset == 0:
		// A full body is acceptable when reading from the start.
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("xiaomidrive: gallery download %s: HTTP %d", id, resp.StatusCode)
	}
	// A storage-node error is HTTP 200 with a small JSON body
	// ({"stat":"ERR_..."}); file bodies come as application/octet-stream.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("xiaomidrive: gallery download %s: storage node error: %s", id, bytes.TrimSpace(msg))
	}
	if length >= 0 {
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(resp.Body, length), resp.Body}, nil
	}
	return resp.Body, nil
}

// Open reads a whole photo or video from the start.
func (g *Gallery) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	return g.Download(ctx, id, 0, -1)
}
