package micloud

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// blockSize is the fixed upload chunk size (4 MiB).
const blockSize = 4 << 20

// UploadOptions tunes an upload. The zero value uploads up to 4 blocks in
// parallel and dates the file with the current time.
type UploadOptions struct {
	// Concurrency is the maximum number of blocks uploaded at once (default 4).
	Concurrency int
	// MimeType overrides the auto-detected content type.
	MimeType string
	// ModTime / CreateTime set the client-side timestamps (default: now).
	ModTime    time.Time
	CreateTime time.Time
	// Progress, if set, is called with bytes uploaded so far.
	Progress func(uploaded int64)
}

func (o UploadOptions) concurrency() int {
	if o.Concurrency <= 0 {
		return 4
	}
	return o.Concurrency
}

type blockSpool struct {
	sha1   string
	md5    string
	size   int64
	offset int64
}

// spooled is content buffered to a temp file with precomputed hashes.
type spooled struct {
	f       *os.File
	size    int64
	sha1    string
	blocks  []blockSpool
	cleanup func()
}

func spoolContent(r io.Reader) (*spooled, error) {
	tmp, err := os.CreateTemp("", "micloud-upload-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}
	whole := sha1.New()
	var blocks []blockSpool
	var total int64
	buf := make([]byte, blockSize)
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := tmp.Write(chunk); werr != nil {
				cleanup()
				return nil, werr
			}
			whole.Write(chunk)
			bs := sha1.Sum(chunk)
			bm := md5.Sum(chunk)
			blocks = append(blocks, blockSpool{
				sha1:   hex.EncodeToString(bs[:]),
				md5:    hex.EncodeToString(bm[:]),
				size:   int64(n),
				offset: total,
			})
			total += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			cleanup()
			return nil, rerr
		}
	}
	return &spooled{
		f:       tmp,
		size:    total,
		sha1:    hex.EncodeToString(whole.Sum(nil)),
		blocks:  blocks,
		cleanup: cleanup,
	}, nil
}

func detectMime(name, override string) string {
	if override != "" {
		return override
	}
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

type createUploadData struct {
	FileName        string         `json:"fileName"`
	Size            int64          `json:"size"`
	SHA1            string         `json:"sha1"`
	MimeType        string         `json:"mimeType"`
	Channel         string         `json:"channel"`
	ParentID        string         `json:"parentId"`
	LocalCreateTime int64          `json:"localCreateTime"`
	LocalModifyTime int64          `json:"localModifyTime"`
	ExifInfo        map[string]any `json:"exifInfo"`
}

type kssBlockInfo struct {
	SHA1 string `json:"sha1"`
	MD5  string `json:"md5"`
	Size int64  `json:"size"`
}

type kssCreate struct {
	BlockInfos []kssBlockInfo `json:"block_infos"`
	SplitSize  int64          `json:"splitSize"`
}

type createResp struct {
	Exists   flexBool   `json:"exists"`
	ID       flexString `json:"id"`
	UploadID string     `json:"uploadId"`
	KSS      struct {
		Stat       string   `json:"stat"`
		NodeURLs   []string `json:"node_urls"`
		FileMeta   string   `json:"file_meta"`
		BlockMetas []struct {
			IsExisted  int    `json:"is_existed"`
			BlockMeta  string `json:"block_meta"`
			CommitMeta string `json:"commit_meta"`
		} `json:"block_metas"`
	} `json:"kss"`
}

type blockResp struct {
	Stat       string `json:"stat"`
	CommitMeta string `json:"commit_meta"`
}

// Upload stores content under parentID as name. It returns the created File.
//
// Behaviour follows the service: content already present server-side is a rapid
// upload (no block transfer); a same-name/different-content upload is
// auto-renamed to "name(1).ext" rather than overwriting; empty files are
// rejected with ErrEmptyFile. To overwrite, use Overwrite.
func (c *Client) Upload(ctx context.Context, parentID, name string, r io.Reader, opts UploadOptions) (*File, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	sp, err := spoolContent(r)
	if err != nil {
		return nil, err
	}
	defer sp.cleanup()
	if sp.size == 0 {
		return nil, ErrEmptyFile
	}

	now := time.Now()
	mt, ct := opts.ModTime, opts.CreateTime
	if mt.IsZero() {
		mt = now
	}
	if ct.IsZero() {
		ct = now
	}

	data := createUploadData{
		FileName: name, Size: sp.size, SHA1: sp.sha1,
		MimeType: detectMime(name, opts.MimeType), Channel: "WEB", ParentID: parentID,
		LocalCreateTime: ct.UnixMilli(), LocalModifyTime: mt.UnixMilli(), ExifInfo: map[string]any{},
	}
	kss := kssCreate{SplitSize: blockSize}
	for _, b := range sp.blocks {
		kss.BlockInfos = append(kss.BlockInfos, kssBlockInfo{SHA1: b.sha1, MD5: b.md5, Size: b.size})
	}
	dataJSON, _ := json.Marshal(data)
	kssJSON, _ := json.Marshal(kss)

	form := url.Values{
		"data": {string(dataJSON)}, "kss": {string(kssJSON)},
		"temp": {"false"}, "isClientUploadThumbnail": {"false"},
	}
	raw, err := c.s.postForm(ctx, "/drive/v2/user/files/create", form)
	if err != nil {
		return nil, err
	}
	var cr createResp
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("micloud: upload create decode: %w", err)
	}

	// Rapid upload: the service already has this content and created the file.
	if bool(cr.Exists) {
		if cr.ID != "" {
			return c.Get(ctx, string(cr.ID))
		}
		return nil, fmt.Errorf("micloud: upload: rapid upload reported no id")
	}
	if len(cr.KSS.NodeURLs) == 0 || cr.UploadID == "" {
		return nil, fmt.Errorf("micloud: upload: incomplete create response")
	}
	if n := len(cr.KSS.BlockMetas); n != len(sp.blocks) {
		return nil, fmt.Errorf("micloud: upload: server expected %d blocks, have %d", n, len(sp.blocks))
	}

	commitMetas, err := c.uploadBlocks(ctx, sp, &cr, opts)
	if err != nil {
		return nil, err
	}

	type commitMeta struct {
		CommitMeta string `json:"commit_meta"`
	}
	cm := make([]commitMeta, len(commitMetas))
	for i, m := range commitMetas {
		cm[i] = commitMeta{CommitMeta: m}
	}
	cmJSON, _ := json.Marshal(cm)
	commitForm := url.Values{
		"uploadId": {cr.UploadID}, "file_meta": {cr.KSS.FileMeta},
		"commit_metas": {string(cmJSON)}, "parentId": {parentID},
		"temp": {"false"}, "isClientUploadThumbnail": {"false"},
	}
	raw, err = c.s.postForm(ctx, "/drive/v2/user/files/commit", commitForm)
	if err != nil {
		return nil, err
	}
	var commit struct {
		ID flexString `json:"id"`
	}
	if err := json.Unmarshal(raw, &commit); err != nil {
		return nil, fmt.Errorf("micloud: upload commit decode: %w", err)
	}
	return c.Get(ctx, string(commit.ID))
}

// uploadBlocks uploads every block (concurrently) and returns their commit
// metas in block order.
func (c *Client) uploadBlocks(ctx context.Context, sp *spooled, cr *createResp, opts UploadOptions) ([]string, error) {
	node := strings.TrimRight(cr.KSS.NodeURLs[0], "/")
	commitMetas := make([]string, len(sp.blocks))

	var (
		mu       sync.Mutex
		uploaded int64
		firstErr error
	)
	sem := make(chan struct{}, opts.concurrency())
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i := range sp.blocks {
		meta := cr.KSS.BlockMetas[i]
		if meta.IsExisted == 1 && meta.CommitMeta != "" {
			commitMetas[i] = meta.CommitMeta
			continue
		}
		wg.Add(1)
		go func(i int, blockMeta string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			cm, err := c.uploadOneBlock(ctx, node, cr.KSS.FileMeta, blockMeta, sp, sp.blocks[i])
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			commitMetas[i] = cm
			uploaded += sp.blocks[i].size
			if opts.Progress != nil {
				opts.Progress(uploaded)
			}
		}(i, meta.BlockMeta)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	for i, m := range commitMetas {
		if m == "" {
			return nil, fmt.Errorf("micloud: upload: block %d produced no commit meta", i)
		}
	}
	return commitMetas, nil
}

func (c *Client) uploadOneBlock(ctx context.Context, node, fileMeta, blockMeta string, sp *spooled, b blockSpool) (string, error) {
	data := make([]byte, b.size)
	if _, err := sp.f.ReadAt(data, b.offset); err != nil && err != io.EOF {
		return "", err
	}
	// The web client sends "chunk_pos=0&&file_meta=..." verbatim (two
	// ampersands); the metas are URL-safe tokens, sent unescaped.
	rawURL := fmt.Sprintf("%s/upload_block_chunk?chunk_pos=0&&file_meta=%s&block_meta=%s", node, fileMeta, blockMeta)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var br blockResp
	if err := json.Unmarshal(body, &br); err != nil {
		return "", fmt.Errorf("micloud: block upload decode (HTTP %d): %w", resp.StatusCode, err)
	}
	if br.Stat != "BLOCK_COMPLETED" || br.CommitMeta == "" {
		return "", fmt.Errorf("micloud: block upload: stat=%q", br.Stat)
	}
	return br.CommitMeta, nil
}

// Overwrite replaces the content of the file at name under parentID, or creates
// it if absent. Because the service has no in-place update, this uploads to a
// temporary name, then renames over the old file (which goes to the recycle
// bin). The returned File has a new ID.
func (c *Client) Overwrite(ctx context.Context, parentID, name string, r io.Reader, opts UploadOptions) (*File, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	// Find an existing same-name file (case-insensitive) to replace.
	var old *File
	if children, err := c.List(ctx, FolderRef(parentID)); err == nil {
		for _, ch := range children {
			if !ch.IsDir() && strings.EqualFold(ch.Name, name) {
				old = ch
				break
			}
		}
	}

	tmpName := fmt.Sprintf("%s.tmp-%d", name, time.Now().UnixNano())
	uploaded, err := c.Upload(ctx, parentID, tmpName, r, opts)
	if err != nil {
		return nil, err
	}
	if old != nil {
		if err := c.Remove(ctx, []*File{old}); err != nil {
			return nil, fmt.Errorf("micloud: overwrite: remove old: %w", err)
		}
	}
	if err := c.Rename(ctx, uploaded.ID, name); err != nil {
		return nil, fmt.Errorf("micloud: overwrite: rename: %w", err)
	}
	uploaded.Name = name
	return uploaded, nil
}
