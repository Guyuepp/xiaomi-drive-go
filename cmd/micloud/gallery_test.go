package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Guyuepp/xiaomi-drive-go"
)

// ---------------------------------------------------------------------------
// fake gallery
// ---------------------------------------------------------------------------

type fakeGallery struct {
	mu sync.Mutex

	albums  []*xiaomidrive.Album
	photos  []*xiaomidrive.Photo // overview, in listing order
	private []*xiaomidrive.Photo // AlbumPrivate, not in the overview
	trash   []*xiaomidrive.Photo
	content map[string][]byte // photo ID -> bytes served by Open

	// Behaviour overrides.
	albumsErr   error
	openErr     map[string]error
	uploadName  string // name the "service" gives the upload; "" keeps it
	uploadErr   error
	deleteAlbum error

	// Recorded calls.
	walkQueries []xiaomidrive.PhotoQuery
	libraryWalk int
	opened      []string
	upload      *fakeUpload
	calls       []string
}

type fakeUpload struct {
	albumID, name string
	data          []byte
	opts          xiaomidrive.GalleryUploadOptions
}

func (f *fakeGallery) record(format string, a ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeGallery) Albums(context.Context) ([]*xiaomidrive.Album, error) {
	if f.albumsErr != nil {
		return nil, f.albumsErr
	}
	return f.albums, nil
}

func (f *fakeGallery) WalkPhotos(_ context.Context, q xiaomidrive.PhotoQuery, fn func(*xiaomidrive.Photo) error) error {
	f.mu.Lock()
	f.walkQueries = append(f.walkQueries, q)
	f.mu.Unlock()
	src := f.photos
	if q.AlbumID == xiaomidrive.AlbumPrivate {
		src = f.private
	}
	for _, p := range src {
		if q.AlbumID != "" && q.AlbumID != xiaomidrive.AlbumPrivate && p.AlbumID != q.AlbumID {
			continue
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeGallery) WalkLibrary(ctx context.Context, fn func(*xiaomidrive.Photo) error) error {
	f.mu.Lock()
	f.libraryWalk++
	f.mu.Unlock()
	for _, p := range append(append([]*xiaomidrive.Photo{}, f.photos...), f.private...) {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeGallery) Open(_ context.Context, id string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opened = append(f.opened, id)
	f.mu.Unlock()
	if err := f.openErr[id]; err != nil {
		return nil, err
	}
	b, ok := f.content[id]
	if !ok {
		return nil, xiaomidrive.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeGallery) UploadReaderAt(_ context.Context, albumID, name string, ra io.ReaderAt, size int64, opts xiaomidrive.GalleryUploadOptions) (*xiaomidrive.Photo, error) {
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	data := make([]byte, size)
	if _, err := ra.ReadAt(data, 0); err != nil && err != io.EOF {
		return nil, err
	}
	f.upload = &fakeUpload{albumID: albumID, name: name, data: data, opts: opts}
	if opts.Progress != nil {
		opts.Progress(size)
	}
	got := name
	if f.uploadName != "" {
		got = f.uploadName
	}
	taken := opts.DateTaken
	if taken.IsZero() {
		taken = opts.ModTime
	}
	return &xiaomidrive.Photo{ID: "new-photo", AlbumID: albumID, Name: got, Size: size, Taken: taken}, nil
}

func (f *fakeGallery) CreateAlbum(_ context.Context, name string) (*xiaomidrive.Album, error) {
	f.record("CreateAlbum %s", name)
	return &xiaomidrive.Album{ID: "a-new", Name: name}, nil
}

func (f *fakeGallery) RenameAlbum(_ context.Context, id, newName string) (*xiaomidrive.Album, error) {
	f.record("RenameAlbum %s %s", id, newName)
	return &xiaomidrive.Album{ID: id, Name: newName}, nil
}

func (f *fakeGallery) DeleteAlbum(_ context.Context, id string) error {
	f.record("DeleteAlbum %s", id)
	return f.deleteAlbum
}

func (f *fakeGallery) Move(_ context.Context, id, albumID string) (*xiaomidrive.Photo, error) {
	f.record("Move %s %s", id, albumID)
	return &xiaomidrive.Photo{ID: id + "-moved", AlbumID: albumID}, nil
}

func (f *fakeGallery) Copy(_ context.Context, id, albumID string) (*xiaomidrive.Photo, error) {
	f.record("Copy %s %s", id, albumID)
	return &xiaomidrive.Photo{ID: id + "-copy", AlbumID: albumID}, nil
}

func (f *fakeGallery) Delete(_ context.Context, id string) error {
	f.record("Delete %s", id)
	return nil
}

func (f *fakeGallery) Trash(context.Context) ([]*xiaomidrive.Photo, error) {
	return f.trash, nil
}

func (f *fakeGallery) Restore(_ context.Context, p *xiaomidrive.Photo) (*xiaomidrive.Photo, error) {
	f.record("Restore %s tag=%s", p.ID, p.Tag)
	return p, nil
}

func (f *fakeGallery) Purge(_ context.Context, id string) error {
	f.record("Purge %s", id)
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestCmd(g *fakeGallery) (*galleryCmd, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &galleryCmd{g: g, out: &out, errOut: &errOut}, &out, &errOut
}

func run(t *testing.T, g *fakeGallery, args ...string) (string, string, error) {
	t.Helper()
	c, out, errOut := newTestCmd(g)
	err := c.run(context.Background(), args)
	return out.String(), errOut.String(), err
}

func sha1Hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}

// mediaPhoto builds a photo whose content is registered in g.
func mediaPhoto(g *fakeGallery, id, album, name string, data []byte, taken time.Time) *xiaomidrive.Photo {
	if g.content == nil {
		g.content = map[string][]byte{}
	}
	g.content[id] = data
	return &xiaomidrive.Photo{
		ID: id, AlbumID: album, Name: name, Type: xiaomidrive.MediaImage,
		Size: int64(len(data)), SHA1: sha1Hex(data), Taken: taken,
	}
}

var testAlbums = []*xiaomidrive.Album{
	{ID: "1", Name: "相机", MediaCount: 2},
	{ID: "a-trip", Name: "Trip", MediaCount: 1},
	{ID: "a-num", Name: "2", MediaCount: 0}, // an album literally named "2"
}

// ---------------------------------------------------------------------------
// pure helpers
// ---------------------------------------------------------------------------

func TestParseDay(t *testing.T) {
	d, err := parseDay("2001-02-03")
	if err != nil {
		t.Fatal(err)
	}
	if got := d.In(xiaomidrive.GalleryZone).Format("2006-01-02 15:04 -0700"); got != "2001-02-03 00:00 +0800" {
		t.Errorf("parseDay = %s, want midnight UTC+8", got)
	}
	for _, bad := range []string{"", "2001/02/03", "20010203", "2001-13-01"} {
		if _, err := parseDay(bad); err == nil {
			t.Errorf("parseDay(%q) accepted", bad)
		}
	}
}

func TestParseDateTime(t *testing.T) {
	cases := map[string]string{
		"2001-02-03 04:05:06": "2001-02-03 04:05:06",
		"2001-02-03 04:05":    "2001-02-03 04:05:00",
		"2001-02-03":          "2001-02-03 00:00:00",
	}
	for in, want := range cases {
		got, err := parseDateTime(in)
		if err != nil {
			t.Errorf("parseDateTime(%q): %v", in, err)
			continue
		}
		if got.Location() != time.Local {
			t.Errorf("parseDateTime(%q) zone %v, want local", in, got.Location())
		}
		if s := got.Format("2006-01-02 15:04:05"); s != want {
			t.Errorf("parseDateTime(%q) = %s, want %s", in, s, want)
		}
	}
	for _, bad := range []string{"", "yesterday", "2001-02-03T04:05:06Z", "03/02/2001"} {
		if _, err := parseDateTime(bad); err == nil {
			t.Errorf("parseDateTime(%q) accepted", bad)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a.jpg", "a.jpg"},
		{"  spaced.jpg ", "spaced.jpg"},
		{"../etc/passwd", ".._etc_passwd"},
		{`a\b`, "a_b"},
		{"nul\x00byte", "nulbyte"},
		{"..", "fb"},
		{".", "fb"},
		{"", "fb"},
		{"   ", "fb"},
		{"照片", "照片"},
	}
	for _, c := range cases {
		if got := safeName(c.in, "fb"); got != c.want {
			t.Errorf("safeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPlanBackup(t *testing.T) {
	photos := []*xiaomidrive.Photo{
		{ID: "p1", AlbumID: "1", Name: "IMG_1.jpg"},
		{ID: "p2", AlbumID: "1", Name: "img_1.JPG"}, // case-insensitive clash
		{ID: "p3", AlbumID: "a-trip", Name: "IMG_1.jpg"},
		{ID: "p4", AlbumID: "unknown", Name: "x.png"},
		{ID: "p5", AlbumID: "evil", Name: "../../escape.jpg"},
		{ID: "p6", AlbumID: "1", Name: ""},
	}
	names := map[string]string{"1": "相机", "a-trip": "Trip", "evil": ".."}
	items := planBackup("/bk", photos, names)

	want := []string{
		"/bk/相机/IMG_1.jpg",
		"/bk/相机/img_1_p2.JPG",
		"/bk/Trip/IMG_1.jpg",
		"/bk/unknown/x.png",
		"/bk/evil/.._.._escape.jpg",
		"/bk/相机/p6",
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d", len(items), len(want))
	}
	for i, it := range items {
		if it.path != want[i] {
			t.Errorf("item %d (%s): path %q, want %q", i, it.photo.ID, it.path, want[i])
		}
		if it.photo != photos[i] {
			t.Errorf("item %d: wrong photo", i)
		}
		if rel, err := filepath.Rel("/bk", it.path); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("item %d escapes the backup dir: %s", i, it.path)
		}
	}
}

// ---------------------------------------------------------------------------
// album resolution and simple commands
// ---------------------------------------------------------------------------

func TestResolveAlbum(t *testing.T) {
	c, _, _ := newTestCmd(&fakeGallery{albums: testAlbums})
	ctx := context.Background()
	cases := []struct{ arg, wantID string }{
		{"trip", "a-trip"},   // name, case-insensitive
		{"a-trip", "a-trip"}, // ID
		{"2", "a-num"},       // name wins over the screenshots ID
		{"1", "1"},
		{"1000", "1000"}, // system album not in the list
	}
	for _, tc := range cases {
		a, err := c.resolveAlbum(ctx, tc.arg)
		if err != nil {
			t.Errorf("resolveAlbum(%q): %v", tc.arg, err)
			continue
		}
		if a.ID != tc.wantID {
			t.Errorf("resolveAlbum(%q) = %s, want %s", tc.arg, a.ID, tc.wantID)
		}
	}
	if a, _ := c.resolveAlbum(ctx, "1000"); a == nil || a.Name != "私密" {
		t.Errorf("private album fallback name = %v", a)
	}
	if _, err := c.resolveAlbum(ctx, "nope"); !errors.Is(err, xiaomidrive.ErrNotFound) {
		t.Errorf("unknown album: %v, want ErrNotFound", err)
	}

	boom := errors.New("boom")
	c2, _, _ := newTestCmd(&fakeGallery{albumsErr: boom})
	if _, err := c2.resolveAlbum(ctx, "trip"); !errors.Is(err, boom) {
		t.Errorf("list error not passed through: %v", err)
	}
}

func TestRunUnknownAndBlocked(t *testing.T) {
	if _, _, err := run(t, &fakeGallery{}, "frobnicate"); err == nil || !strings.Contains(err.Error(), "frobnicate") {
		t.Errorf("unknown subcommand: %v", err)
	}
	_, _, err := run(t, &fakeGallery{albumsErr: xiaomidrive.ErrBlocked}, "albums")
	if !errors.Is(err, xiaomidrive.ErrBlocked) || !strings.Contains(err.Error(), "过一段时间再试") {
		t.Errorf("blocked: %v", err)
	}
	out, _, err := run(t, &fakeGallery{}, "help")
	if err != nil || !strings.Contains(out, "micloud gallery backup") {
		t.Errorf("help: %v %q", err, out)
	}
}

func TestAlbums(t *testing.T) {
	out, _, err := run(t, &fakeGallery{albums: testAlbums}, "albums")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "a-trip") || !strings.HasSuffix(lines[1], "Trip") {
		t.Errorf("albums output:\n%s", out)
	}
}

func TestLs(t *testing.T) {
	g := &fakeGallery{albums: testAlbums}
	taken := time.Date(2001, 2, 3, 4, 5, 0, 0, xiaomidrive.GalleryZone)
	g.photos = []*xiaomidrive.Photo{
		{ID: "p1", AlbumID: "a-trip", Name: "beach.jpg", Type: xiaomidrive.MediaImage, Size: 2048, Taken: taken},
		{ID: "p2", AlbumID: "a-trip", Name: "clip.mp4", Type: xiaomidrive.MediaVideo, Size: 10, Taken: taken},
		{ID: "p3", AlbumID: "1", Name: "other.jpg", Type: xiaomidrive.MediaImage, Size: 1, Taken: taken},
	}

	out, _, err := run(t, g, "ls", "trip", "--from", "2001-01-01", "--to", "2001-12-31", "--video")
	if err != nil {
		t.Fatal(err)
	}
	q := g.walkQueries[0]
	if q.AlbumID != "a-trip" || !q.OnlyVideo {
		t.Errorf("query = %+v", q)
	}
	if got := q.Start.In(xiaomidrive.GalleryZone).Format("2006-01-02"); got != "2001-01-01" {
		t.Errorf("start = %s", got)
	}
	if got := q.End.In(xiaomidrive.GalleryZone).Format("2006-01-02"); got != "2001-12-31" {
		t.Errorf("end = %s", got)
	}
	// The fake does not filter by video; the CLI prints what it gets.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ls output:\n%s", out)
	}
	f1, f2 := strings.Fields(lines[0]), strings.Fields(lines[1])
	if strings.Join(f1, " ") != "p1 img 2001-02-03 04:05 2.0 KiB beach.jpg" || f2[1] != "vid" {
		t.Errorf("ls output:\n%s", out)
	}

	g.walkQueries = nil
	if _, _, err := run(t, g, "ls"); err != nil || g.walkQueries[0] != (xiaomidrive.PhotoQuery{}) {
		t.Errorf("plain ls: err %v query %+v", err, g.walkQueries)
	}
	if _, _, err := run(t, g, "ls", "--from", "2001-02-30"); err == nil {
		t.Error("bad date accepted")
	}
	if _, _, err := run(t, g, "ls", "trip", "extra"); err == nil {
		t.Error("extra argument accepted")
	}
	if _, _, err := run(t, g, "ls", "nope"); !errors.Is(err, xiaomidrive.ErrNotFound) {
		t.Errorf("unknown album: %v", err)
	}
}

func TestGet(t *testing.T) {
	dir := t.TempDir()
	g := &fakeGallery{content: map[string][]byte{"p1": []byte("hello")}}
	out := filepath.Join(dir, "a.jpg")

	stdout, _, err := run(t, g, "get", "p1", "-o", out)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "hello" {
		t.Errorf("file content %q", b)
	}
	if !strings.Contains(stdout, "已下载 p1") {
		t.Errorf("output %q", stdout)
	}
	// An existing file is never overwritten.
	if _, _, err := run(t, g, "get", "p1", "-o", out); !errors.Is(err, os.ErrExist) {
		t.Errorf("overwrite: %v, want ErrExist", err)
	}
	if _, _, err := run(t, g, "get", "missing", "-o", filepath.Join(dir, "m")); !errors.Is(err, xiaomidrive.ErrNotFound) {
		t.Errorf("missing photo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m")); !os.IsNotExist(err) {
		t.Error("file created for a failed download")
	}
	if _, _, err := run(t, g, "get"); err == nil {
		t.Error("get without id accepted")
	}
}

// failingReader returns some bytes, then an error.
type failingReader struct{ sent bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("connection reset")
}

type failingGallery struct{ fakeGallery }

func (f *failingGallery) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(&failingReader{}), nil
}

func TestGetRemovesPartialFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "a.jpg")
	c := &galleryCmd{g: &failingGallery{}, out: io.Discard, errOut: io.Discard}
	if err := c.run(context.Background(), []string{"get", "p1", "-o", out}); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("partial file left behind")
	}
}

func TestPut(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "IMG_1.jpg")
	if err := os.WriteFile(local, []byte("jpegdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2010, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(local, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	g := &fakeGallery{albums: testAlbums}
	stdout, stderr, err := run(t, g, "put", local, "trip")
	if err != nil {
		t.Fatal(err)
	}
	u := g.upload
	if u.albumID != "a-trip" || u.name != "IMG_1.jpg" || string(u.data) != "jpegdata" {
		t.Errorf("upload = %+v", u)
	}
	if !u.opts.ModTime.Equal(mtime) || !u.opts.DateTaken.IsZero() {
		t.Errorf("dates: mod %v taken %v; want file mtime and no DateTaken", u.opts.ModTime, u.opts.DateTaken)
	}
	if !strings.Contains(stderr, "100%") {
		t.Errorf("no progress on stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "Trip/IMG_1.jpg") || strings.Contains(stdout, "改了名") {
		t.Errorf("output %q", stdout)
	}

	// --date and --name; the service renames the upload.
	g.uploadName = "x_1793452582173.jpg"
	stdout, _, err = run(t, g, "put", local, "a-trip", "--name", "x.jpg", "--date", "2003-04-05 12:00")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2003, 4, 5, 12, 0, 0, 0, time.Local)
	if !g.upload.opts.DateTaken.Equal(want) || g.upload.name != "x.jpg" {
		t.Errorf("upload %q taken %v, want x.jpg at %v", g.upload.name, g.upload.opts.DateTaken, want)
	}
	if !strings.Contains(stdout, "x_1793452582173.jpg") || !strings.Contains(stdout, "改了名") {
		t.Errorf("rename not reported: %q", stdout)
	}
	g.uploadName = ""

	// Server-side rename that only changes case is not a rename.
	g.uploadName = "img_1.JPG"
	stdout, _, _ = run(t, g, "put", local, "trip")
	if strings.Contains(stdout, "改了名") {
		t.Errorf("case-only difference reported as rename: %q", stdout)
	}
	g.uploadName = ""

	if _, _, err := run(t, g, "put", local, "trip", "--date", "tomorrow"); err == nil {
		t.Error("bad --date accepted")
	}
	if _, _, err := run(t, g, "put", dir, "trip"); err == nil || !strings.Contains(err.Error(), "目录") {
		t.Errorf("directory upload: %v", err)
	}
	if _, _, err := run(t, g, "put", local); err == nil {
		t.Error("missing album accepted")
	}
	if _, _, err := run(t, g, "put", local, "trip", "extra"); err == nil {
		t.Error("extra argument accepted")
	}
	if _, _, err := run(t, g, "put", local, "nope"); !errors.Is(err, xiaomidrive.ErrNotFound) {
		t.Errorf("unknown album: %v", err)
	}
	g.uploadErr = xiaomidrive.ErrEmptyFile
	if _, _, err := run(t, g, "put", local, "trip"); err == nil || !strings.Contains(err.Error(), "空文件") {
		t.Errorf("empty file message: %v", err)
	}
}

func TestAlbumCommands(t *testing.T) {
	g := &fakeGallery{albums: testAlbums}
	if out, _, err := run(t, g, "mkalbum", "New"); err != nil || !strings.Contains(out, "a-new") {
		t.Errorf("mkalbum: %v %q", err, out)
	}
	if out, _, err := run(t, g, "rename-album", "trip", "Trip 2026"); err != nil || !strings.Contains(out, "Trip 2026") {
		t.Errorf("rename-album: %v %q", err, out)
	}
	if out, _, err := run(t, g, "rmalbum", "Trip"); err != nil || !strings.Contains(out, "已删除相册 Trip") {
		t.Errorf("rmalbum: %v %q", err, out)
	}
	wantCalls := []string{"CreateAlbum New", "RenameAlbum a-trip Trip 2026", "DeleteAlbum a-trip"}
	if strings.Join(g.calls, "|") != strings.Join(wantCalls, "|") {
		t.Errorf("calls = %q, want %q", g.calls, wantCalls)
	}

	g.deleteAlbum = xiaomidrive.ErrAlbumNotEmpty
	if _, _, err := run(t, g, "rmalbum", "trip"); err == nil || !strings.Contains(err.Error(), "不是空的") {
		t.Errorf("non-empty album message: %v", err)
	}
	for _, args := range [][]string{{"mkalbum"}, {"mkalbum", "a", "b"}, {"rename-album", "trip"}, {"rmalbum"}} {
		if _, _, err := run(t, g, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestPhotoCommands(t *testing.T) {
	g := &fakeGallery{albums: testAlbums}
	if out, _, err := run(t, g, "mv", "p1", "trip"); err != nil || !strings.Contains(out, "已移动到 Trip,新 id p1-moved") {
		t.Errorf("mv: %v %q", err, out)
	}
	if out, _, err := run(t, g, "cp", "p1", "相机"); err != nil || !strings.Contains(out, "已复制到 相机,新 id p1-copy") {
		t.Errorf("cp: %v %q", err, out)
	}
	if _, _, err := run(t, g, "rm", "p9"); err != nil {
		t.Errorf("rm: %v", err)
	}
	wantCalls := []string{"Move p1 a-trip", "Copy p1 1", "Delete p9"}
	if strings.Join(g.calls, "|") != strings.Join(wantCalls, "|") {
		t.Errorf("calls = %q, want %q", g.calls, wantCalls)
	}
	if _, _, err := run(t, g, "mv", "p1", "nope"); !errors.Is(err, xiaomidrive.ErrNotFound) {
		t.Errorf("mv to unknown album: %v", err)
	}
	for _, args := range [][]string{{"mv", "p1"}, {"cp"}, {"rm"}, {"rm", "a", "b"}} {
		if _, _, err := run(t, g, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestTrashCommands(t *testing.T) {
	deleted := time.Date(2026, 10, 31, 12, 0, 0, 0, xiaomidrive.GalleryZone)
	g := &fakeGallery{trash: []*xiaomidrive.Photo{
		{ID: "t1", Name: "gone.jpg", Size: 3, Tag: "tag-1", Deleted: deleted},
	}}
	out, _, err := run(t, g, "trash", "ls")
	if err != nil || !strings.Contains(out, "t1") || !strings.Contains(out, "2026-10-31 12:00") || !strings.Contains(out, "gone.jpg") {
		t.Errorf("trash ls: %v %q", err, out)
	}
	// restore looks the item up to pass its tag.
	if _, _, err := run(t, g, "trash", "restore", "t1"); err != nil {
		t.Errorf("restore: %v", err)
	}
	if _, _, err := run(t, g, "trash", "restore", "t2"); !errors.Is(err, xiaomidrive.ErrNotFound) {
		t.Errorf("restore missing: %v", err)
	}
	if _, _, err := run(t, g, "trash", "purge", "t1"); err != nil {
		t.Errorf("purge: %v", err)
	}
	wantCalls := []string{"Restore t1 tag=tag-1", "Purge t1"}
	if strings.Join(g.calls, "|") != strings.Join(wantCalls, "|") {
		t.Errorf("calls = %q, want %q", g.calls, wantCalls)
	}
	for _, args := range [][]string{{"trash"}, {"trash", "restore"}, {"trash", "purge"}, {"trash", "empty"}} {
		if _, _, err := run(t, g, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

// ---------------------------------------------------------------------------
// backup
// ---------------------------------------------------------------------------

func backupFixture() *fakeGallery {
	g := &fakeGallery{albums: testAlbums}
	t1 := time.Date(2001, 2, 3, 4, 5, 6, 0, xiaomidrive.GalleryZone)
	t2 := time.Date(2002, 2, 3, 4, 5, 6, 0, xiaomidrive.GalleryZone)
	g.photos = []*xiaomidrive.Photo{
		mediaPhoto(g, "p1", "1", "IMG_1.jpg", []byte("one"), t1),
		mediaPhoto(g, "p2", "a-trip", "beach.jpg", []byte("two!"), t2),
	}
	g.private = []*xiaomidrive.Photo{
		mediaPhoto(g, "p3", xiaomidrive.AlbumPrivate, "secret.jpg", []byte("three"), t1),
	}
	return g
}

func TestBackup(t *testing.T) {
	dir := t.TempDir()
	g := backupFixture()

	stdout, _, err := run(t, g, "backup", dir)
	if err != nil {
		t.Fatal(err)
	}
	if g.libraryWalk != 1 {
		t.Errorf("default backup should walk the whole library (overview + private), walks = %d", g.libraryWalk)
	}
	files := map[string]string{
		"相机/IMG_1.jpg":   "one",
		"Trip/beach.jpg": "two!",
		"私密/secret.jpg":  "three",
	}
	for rel, want := range files {
		p := filepath.Join(dir, rel)
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v, want %q", rel, b, err, want)
		}
	}
	fi, _ := os.Stat(filepath.Join(dir, "相机/IMG_1.jpg"))
	if fi != nil && !fi.ModTime().Equal(g.photos[0].Taken) {
		t.Errorf("mtime %v, want taken date %v", fi.ModTime(), g.photos[0].Taken)
	}
	if !strings.Contains(stdout, "下载 3,跳过(已存在)0,失败 0") {
		t.Errorf("summary %q", stdout)
	}
	assertNoPart(t, dir)

	// Second run: everything is skipped without being downloaded again.
	g.opened = nil
	stdout, _, err = run(t, g, "backup", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.opened) != 0 || !strings.Contains(stdout, "下载 0,跳过(已存在)3") {
		t.Errorf("rerun opened %v, summary %q", g.opened, stdout)
	}

	// A local file of the wrong size is replaced.
	target := filepath.Join(dir, "Trip/beach.jpg")
	os.WriteFile(target, []byte("x"), 0o644)
	g.opened = nil
	if _, _, err := run(t, g, "backup", dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "two!" || len(g.opened) != 1 {
		t.Errorf("wrong-size file not replaced: %q, opened %v", b, g.opened)
	}
}

func TestBackupScopes(t *testing.T) {
	g := backupFixture()
	if _, _, err := run(t, g, "backup", t.TempDir(), "--skip-private"); err != nil {
		t.Fatal(err)
	}
	if g.libraryWalk != 0 || len(g.walkQueries) != 1 || g.walkQueries[0] != (xiaomidrive.PhotoQuery{}) {
		t.Errorf("--skip-private: library walks %d, queries %+v", g.libraryWalk, g.walkQueries)
	}
	if len(g.opened) != 2 {
		t.Errorf("--skip-private downloaded %v", g.opened)
	}

	g = backupFixture()
	dir := t.TempDir()
	if _, _, err := run(t, g, "backup", dir, "--album", "trip"); err != nil {
		t.Fatal(err)
	}
	if len(g.walkQueries) != 1 || g.walkQueries[0].AlbumID != "a-trip" || len(g.opened) != 1 || g.opened[0] != "p2" {
		t.Errorf("--album: queries %+v opened %v", g.walkQueries, g.opened)
	}

	for _, args := range [][]string{
		{"backup"}, {"backup", "a", "b"}, {"backup", "d", "-j", "0"}, {"backup", "d", "-j", "9"}, {"backup", "d", "-j", "x"},
	} {
		if _, _, err := run(t, backupFixture(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestBackupVerifiesContent(t *testing.T) {
	dir := t.TempDir()
	g := backupFixture()
	g.photos[0].SHA1 = strings.Repeat("0", 40) // record disagrees with bytes
	g.photos[1].Size = 99                      // short download

	stdout, stderr, err := run(t, g, "backup", dir, "--skip-private")
	if err == nil || !strings.Contains(err.Error(), "2 项失败") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(stderr, "SHA-1") || !strings.Contains(stderr, "应为 99") {
		t.Errorf("failures not reported: %q", stderr)
	}
	if !strings.Contains(stdout, "失败 2") {
		t.Errorf("summary %q", stdout)
	}
	for _, rel := range []string{"相机/IMG_1.jpg", "Trip/beach.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Errorf("%s kept despite failed check", rel)
		}
	}
	assertNoPart(t, dir)
}

func TestBackupStopsWhenBlocked(t *testing.T) {
	dir := t.TempDir()
	g := backupFixture()
	g.openErr = map[string]error{"p1": xiaomidrive.ErrBlocked}

	_, stderr, err := run(t, g, "backup", dir)
	if !errors.Is(err, xiaomidrive.ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if strings.Count(stderr, "停止备份") != 1 {
		t.Errorf("block notice: %q", stderr)
	}
	if len(g.opened) != 1 {
		t.Errorf("kept downloading after the block: %v", g.opened)
	}
	assertNoPart(t, dir)
}

func TestBackupOtherErrorContinues(t *testing.T) {
	dir := t.TempDir()
	g := backupFixture()
	g.openErr = map[string]error{"p1": errors.New("network down")}

	_, stderr, err := run(t, g, "backup", dir, "-j", "2")
	if err == nil || !strings.Contains(err.Error(), "1 项失败") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(stderr, "network down") {
		t.Errorf("failure not reported: %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "私密/secret.jpg")); err != nil {
		t.Errorf("later items not downloaded: %v", err)
	}
}

// cancelGallery cancels the context when the first photo is opened.
type cancelGallery struct {
	*fakeGallery
	cancel context.CancelFunc
}

func (c *cancelGallery) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	c.cancel()
	return nil, ctx.Err()
}

func TestBackupInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &cancelGallery{fakeGallery: backupFixture(), cancel: cancel}
	c := &galleryCmd{g: g, out: io.Discard, errOut: io.Discard}
	err := c.run(ctx, []string{"backup", t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled so main exits 130", err)
	}
}

func assertNoPart(t *testing.T, dir string) {
	t.Helper()
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".part") {
			t.Errorf("temp file left: %s", p)
		}
		return nil
	})
}
