package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingDownloadReader struct {
	read bool
}

func (r *failingDownloadReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("connection interrupted")
	}
	r.read = true
	return copy(p, "partial"), nil
}

func TestSaveDownloadPreservesTargetUntilVerified(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  func() io.Reader
		size int64
		sum  string
		ok   bool
	}{
		{"interruption", func() io.Reader { return &failingDownloadReader{} }, 20, "", false},
		{"wrong size", func() io.Reader { return strings.NewReader("new") }, 4, "", false},
		{"wrong hash", func() io.Reader { return strings.NewReader("new") }, 3, strings.Repeat("0", 40), false},
		{"success", func() io.Reader { return strings.NewReader("new") }, 3, sha1Hex([]byte("new")), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "file")
			if err := os.WriteFile(out, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := saveDownload(context.Background(), out, tc.src(), tc.size, tc.sum)
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v", err)
			}
			data, err := os.ReadFile(out)
			want := "old"
			if tc.ok {
				want = "new"
			}
			if err != nil || string(data) != want {
				t.Fatalf("target=%q error=%v", data, err)
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatalf("temporary files left: %v", entries)
			}
		})
	}
}

func TestSaveDownloadCanceledAndRenameFailure(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := saveDownload(ctx, filepath.Join(dir, "file"), strings.NewReader("new"), 3, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Renaming a file onto an existing directory must leave no temporary file.
	if _, err := saveDownload(context.Background(), dir, strings.NewReader("new"), 3, ""); err == nil {
		t.Fatal("rename onto a directory succeeded")
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".micloud-download-*.part"))
	if len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestReadLineFromDoesNotConsumeNextLine(t *testing.T) {
	r := strings.NewReader("1\r\n2\nlast")
	for _, want := range []string{"1", "2", "last", ""} {
		got, err := readLineFrom(r)
		if err != nil || got != want {
			t.Fatalf("got=%q want=%q error=%v", got, want, err)
		}
	}
}

func TestParseCookieCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		args        []string
		ok          bool
	}{
		{"stdin", `{"userId":"user","passToken":"synthetic-secret"}`, []string{"--stdin"}, true},
		{"flags", "", []string{"--user", "user", "--service", "token"}, true},
		{"mixed", `{}`, []string{"--stdin", "--user", "user"}, false},
		{"duplicate stdin", `{}`, []string{"--stdin", "--stdin"}, false},
		{"missing flag value", "", []string{"--user"}, false},
		{"flag as value", "", []string{"--user", "--service", "token"}, false},
		{"extra json", `{"userId":"user","passToken":"token"}{}`, []string{"--stdin"}, false},
		{"trailing garbage", `{"userId":"user","passToken":"token"}bad`, []string{"--stdin"}, false},
		{"unknown field", `{"userId":"user","passToken":"token","secret-typo":"synthetic-secret"}`, []string{"--stdin"}, false},
		{"oversize", strings.Repeat(" ", (1<<20)+1), []string{"--stdin"}, false},
		{"no user", `{"passToken":"synthetic-secret"}`, []string{"--stdin"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCookieCredentials(tc.args, strings.NewReader(tc.input))
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("error disclosed a credential")
			}
		})
	}
}
