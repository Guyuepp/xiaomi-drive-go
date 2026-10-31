//go:build e2e

// End-to-end tests against the real service. They are excluded from normal
// builds; run them explicitly with an existing login:
//
//	MICLOUD_CREDENTIALS=~/.config/micloud/credentials.json go test -tags e2e -run E2E -v .
//
// All writes happen inside a fresh folder under /__micloud_probe__, which is
// removed afterwards. Only items this test created are purged from the bin.
package xiaomidrive_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Guyuepp/xiaomi-drive-go"
)

const probeRoot = "__micloud_probe__"

func e2eClient(t *testing.T) *xiaomidrive.Client {
	t.Helper()
	p := os.Getenv("MICLOUD_CREDENTIALS")
	if p == "" {
		t.Skip("MICLOUD_CREDENTIALS not set")
	}
	c := xiaomidrive.NewClient(xiaomidrive.WithStore(&xiaomidrive.FileStore{Path: p}))
	if !c.LoggedIn() {
		t.Skip("no credentials at MICLOUD_CREDENTIALS")
	}
	return c
}

func TestE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := e2eClient(t)

	probe, err := c.MakeDir(ctx, xiaomidrive.RootID, probeRoot)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := c.MakeDir(ctx, probe.ID, fmt.Sprintf("e2e-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	var created []string // file IDs to purge from the bin afterwards
	t.Cleanup(func() {
		cctx := context.Background()
		if err := c.Remove(cctx, []*xiaomidrive.File{dir}); err != nil {
			t.Errorf("cleanup remove: %v", err)
			return
		}
		for _, id := range created {
			if err := c.Purge(cctx, id); err != nil && !errors.Is(err, xiaomidrive.ErrNotFound) {
				t.Errorf("cleanup purge %s: %v", id, err)
			}
		}
	})

	// 5 MiB + 3 bytes: two blocks, the second tiny.
	content := make([]byte, 5<<20+3)
	rand.Read(content)

	f, err := c.Upload(ctx, dir.ID, "data.bin", bytes.NewReader(content), xiaomidrive.UploadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	created = append(created, f.ID)
	if f.Size != int64(len(content)) {
		t.Fatalf("size %d, want %d", f.Size, len(content))
	}

	t.Run("RangeDownload", func(t *testing.T) {
		cases := []struct{ off, n int64 }{{0, 100}, {4<<20 - 6, 12}, {int64(len(content)) - 10, 10}, {4 << 20, -1}}
		for _, tc := range cases {
			rc, err := c.Download(ctx, f.ID, tc.off, tc.n)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			end := int64(len(content))
			if tc.n >= 0 {
				end = tc.off + tc.n
			}
			if !bytes.Equal(got, content[tc.off:end]) {
				t.Errorf("range off=%d n=%d: got %d bytes, mismatch", tc.off, tc.n, len(got))
			}
		}
	})

	t.Run("Overwrite", func(t *testing.T) {
		next := []byte("replacement content\n")
		nf, err := c.Overwrite(ctx, dir.ID, "data.bin", bytes.NewReader(next), xiaomidrive.UploadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, nf.ID)
		if nf.ID == f.ID {
			t.Errorf("overwrite kept the old ID; expected a new file")
		}
		time.Sleep(time.Second) // rename visibility lag
		kids, err := c.List(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(kids) != 1 || kids[0].Name != "data.bin" || kids[0].ID != nf.ID {
			names := []string{}
			for _, k := range kids {
				names = append(names, k.Name)
			}
			t.Fatalf("after overwrite folder holds %v", names)
		}
		rc, err := c.Open(ctx, nf.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(got, next) {
			t.Errorf("overwritten content mismatch")
		}
	})

	t.Run("Renew", func(t *testing.T) {
		before := c.Session().Credentials().ServiceToken
		if err := c.Session().Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if c.Session().Credentials().ServiceToken == before {
			t.Errorf("refresh did not change serviceToken")
		}
		if _, err := c.Quota(ctx); err != nil {
			t.Errorf("quota after refresh: %v", err)
		}
	})

	t.Run("LinkAndCopy", func(t *testing.T) {
		objs, err := c.List(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(objs) != 1 {
			t.Fatalf("list: %d objs", len(objs))
		}
		l, err := c.Link(ctx, objs[0].ID)
		if err != nil || l.URL == "" || !l.Expires.After(time.Now()) {
			t.Fatalf("link: %+v %v", l, err)
		}
		sub, err := c.MakeDir(ctx, dir.ID, "sub")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Copy(ctx, objs[:1], sub.ID); err != nil {
			t.Fatal(err)
		}
		kids, err := c.List(ctx, sub)
		if err != nil || len(kids) != 1 {
			t.Fatalf("copy result: %d kids, %v", len(kids), err)
		}
		created = append(created, kids[0].ID)
	})

	t.Run("Errors", func(t *testing.T) {
		if _, err := c.Get(ctx, "1"); !errors.Is(err, xiaomidrive.ErrNotFound) {
			t.Errorf("Get bogus id: %v, want ErrNotFound", err)
		}
		if _, err := c.Upload(ctx, dir.ID, "empty", bytes.NewReader(nil), xiaomidrive.UploadOptions{}); !errors.Is(err, xiaomidrive.ErrEmptyFile) {
			t.Errorf("empty upload: %v, want ErrEmptyFile", err)
		}
		if err := c.Move(ctx, []*xiaomidrive.File{dir}, dir.ID); !errors.Is(err, xiaomidrive.ErrIntoSelf) {
			t.Errorf("move into self: %v, want ErrIntoSelf", err)
		}
	})
}
