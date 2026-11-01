package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type downloadReader struct {
	ctx context.Context
	r   io.Reader
}

func (r downloadReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// saveDownload replaces the target only after a complete, verified download.
func saveDownload(ctx context.Context, out string, src io.Reader, size int64, sum string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(filepath.Dir(out), ".micloud-download-*.part")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha1.New()
	n, err := io.Copy(io.MultiWriter(f, h), downloadReader{ctx: ctx, r: src})
	if err != nil {
		return n, err
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	if size >= 0 && n != size {
		return n, fmt.Errorf("下载大小 %d，应为 %d", n, size)
	}
	if sum != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sum) {
		return n, fmt.Errorf("下载 SHA-1 与记录不一致")
	}
	if err := f.Sync(); err != nil {
		return n, err
	}
	if err := f.Close(); err != nil {
		return n, err
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	return n, os.Rename(f.Name(), out)
}
