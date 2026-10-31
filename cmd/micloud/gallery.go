package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Guyuepp/xiaomi-drive-go"
)

const galleryUsage = `用法:
  micloud gallery albums                               列出相册
  micloud gallery ls [相册] [--from 日期] [--to 日期] [--video] [--name 子串]
                                                       列照片(默认总览;日期 YYYY-MM-DD,按东八区)
                                                       --name 按文件名过滤(不区分大小写),不指定相册时
                                                       连同私密相册一起列,照片多时要几分钟
  micloud gallery search <关键词> [--pick 编号] [--limit 张数] [--unsorted]
                                                       服务端搜索:分类、地点、时间、人物、相册名。
                                                       不支持文件名和图片里的文字(文件名用 ls --name)。
                                                       默认取完全部结果,按拍摄时间从新到旧显示前
                                                       --limit 张(默认 100,0 表示全部);--unsorted
                                                       按服务端顺序边取边显示,够数就停
  micloud gallery get <照片ID> [-o 输出]                下载照片或视频
  micloud gallery put <本地文件> <相册> [--name 名称] [--date "YYYY-MM-DD HH:MM:SS"]
                                                       上传到相册(默认按文件修改时间归档)
  micloud gallery backup <目录> [--album 相册] [--skip-private] [-j 并发]
                                                       备份到本地,按相册建子目录,已存在的跳过
  micloud gallery mkalbum <名称>                        新建相册(同名时返回已有的)
  micloud gallery rename-album <相册> <新名称>
  micloud gallery rmalbum <相册>                        删除空相册
  micloud gallery mv|cp <照片ID> <相册>                 移动(照片 ID 会变)/复制
  micloud gallery rm <照片ID>                           删除(进相册回收站)
  micloud gallery trash ls|restore <照片ID>|purge <照片ID>

<相册> 可以是相册名或相册 ID。相机 1、截屏 2、私密 1000。`

// galleryAPI is the part of *xiaomidrive.Gallery the CLI uses. Tests supply
// an in-memory implementation.
type galleryAPI interface {
	Albums(ctx context.Context) ([]*xiaomidrive.Album, error)
	WalkPhotos(ctx context.Context, q xiaomidrive.PhotoQuery, fn func(*xiaomidrive.Photo) error) error
	WalkLibrary(ctx context.Context, fn func(*xiaomidrive.Photo) error) error
	Open(ctx context.Context, id string) (io.ReadCloser, error)
	UploadReaderAt(ctx context.Context, albumID, name string, ra io.ReaderAt, size int64, opts xiaomidrive.GalleryUploadOptions) (*xiaomidrive.Photo, error)
	CreateAlbum(ctx context.Context, name string) (*xiaomidrive.Album, error)
	RenameAlbum(ctx context.Context, id, newName string) (*xiaomidrive.Album, error)
	DeleteAlbum(ctx context.Context, id string) error
	Move(ctx context.Context, id, albumID string) (*xiaomidrive.Photo, error)
	Copy(ctx context.Context, id, albumID string) (*xiaomidrive.Photo, error)
	Delete(ctx context.Context, id string) error
	Trash(ctx context.Context) ([]*xiaomidrive.Photo, error)
	Restore(ctx context.Context, p *xiaomidrive.Photo) (*xiaomidrive.Photo, error)
	Purge(ctx context.Context, id string) error
	Suggest(ctx context.Context, query string) ([]*xiaomidrive.SearchSuggestion, error)
	WalkSearch(ctx context.Context, s *xiaomidrive.SearchSuggestion, fn func(*xiaomidrive.Photo) error) error
}

var _ galleryAPI = (*xiaomidrive.Gallery)(nil)

// galleryCmd runs gallery subcommands. Results go to out, progress and
// warnings to errOut. in is read for interactive choices when interactive is
// set (stdin is a terminal).
type galleryCmd struct {
	g           galleryAPI
	out         io.Writer
	errOut      io.Writer
	in          io.Reader
	interactive bool
}

// stdinIsTerminal reports whether stdin is an interactive terminal. Pipes and
// files are not character devices; /dev/null is, so it is ruled out by name.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}

func cmdGallery(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(galleryUsage)
	}
	if h := args[0]; h == "help" || h == "-h" || h == "--help" {
		fmt.Println(galleryUsage)
		return nil
	}
	c, err := authedClient()
	if err != nil {
		return err
	}
	return (&galleryCmd{
		g: c.Gallery(), out: os.Stdout, errOut: os.Stderr,
		in: os.Stdin, interactive: stdinIsTerminal(),
	}).run(ctx, args)
}

func (c *galleryCmd) run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(galleryUsage)
	}
	sub, rest := args[0], args[1:]
	var err error
	switch sub {
	case "albums":
		err = c.albums(ctx)
	case "ls", "list":
		err = c.ls(ctx, rest)
	case "search":
		err = c.search(ctx, rest)
	case "get", "download":
		err = c.get(ctx, rest)
	case "put", "upload":
		err = c.put(ctx, rest)
	case "backup":
		err = c.backup(ctx, rest)
	case "mkalbum":
		err = c.mkalbum(ctx, rest)
	case "rename-album":
		err = c.renameAlbum(ctx, rest)
	case "rmalbum":
		err = c.rmalbum(ctx, rest)
	case "mv", "cp":
		err = c.mvcp(ctx, sub, rest)
	case "rm":
		err = c.rm(ctx, rest)
	case "trash":
		err = c.trash(ctx, rest)
	case "help", "-h", "--help":
		fmt.Fprintln(c.out, galleryUsage)
		return nil
	default:
		return fmt.Errorf("未知子命令 %q\n\n%s", sub, galleryUsage)
	}
	if errors.Is(err, xiaomidrive.ErrBlocked) {
		return fmt.Errorf("%w\n小米暂时拒绝了这个账号的相册请求,过一段时间再试", err)
	}
	return err
}

// systemAlbumNames names the system albums when the album list leaves them out.
var systemAlbumNames = map[string]string{
	xiaomidrive.AlbumCamera:      "相机",
	xiaomidrive.AlbumScreenshots: "截屏",
	xiaomidrive.AlbumPrivate:     "私密",
}

// resolveAlbum accepts an album name (case-insensitive) or ID. Names win over
// IDs, so an album literally named "1" is found by name.
func (c *galleryCmd) resolveAlbum(ctx context.Context, arg string) (*xiaomidrive.Album, error) {
	albums, err := c.g.Albums(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range albums {
		if strings.EqualFold(a.Name, arg) {
			return a, nil
		}
	}
	for _, a := range albums {
		if a.ID == arg {
			return a, nil
		}
	}
	if name, ok := systemAlbumNames[arg]; ok {
		return &xiaomidrive.Album{ID: arg, Name: name}, nil
	}
	return nil, fmt.Errorf("%w: 找不到相册 %q", xiaomidrive.ErrNotFound, arg)
}

func (c *galleryCmd) albums(ctx context.Context) error {
	albums, err := c.g.Albums(ctx)
	if err != nil {
		return err
	}
	for _, a := range albums {
		fmt.Fprintf(c.out, "%-18s %6d  %s\n", a.ID, a.MediaCount, a.Name)
	}
	return nil
}

// parseDay parses YYYY-MM-DD in the gallery's time zone.
func parseDay(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", s, xiaomidrive.GalleryZone)
	if err != nil {
		return time.Time{}, fmt.Errorf("日期 %q 格式应为 YYYY-MM-DD", s)
	}
	return t, nil
}

// parseDateTime parses a local date with optional time.
func parseDateTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("时间 %q 格式应为 \"YYYY-MM-DD[ HH:MM[:SS]]\"(本地时区)", s)
}

func (c *galleryCmd) ls(ctx context.Context, args []string) error {
	var q xiaomidrive.PhotoQuery
	var album, name string
	nameSet := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--from":
			i++
			t, err := parseDay(arg(args, i))
			if err != nil {
				return err
			}
			q.Start = t
		case "--to":
			i++
			t, err := parseDay(arg(args, i))
			if err != nil {
				return err
			}
			q.End = t
		case "--video":
			q.OnlyVideo = true
		case "--name":
			i++
			name, nameSet = strings.TrimSpace(arg(args, i)), true
		default:
			if album != "" {
				return fmt.Errorf("多余的参数 %q", args[i])
			}
			album = args[i]
		}
	}
	if nameSet && name == "" {
		return errors.New("--name 需要一个非空的子串")
	}
	if album != "" {
		a, err := c.resolveAlbum(ctx, album)
		if err != nil {
			return err
		}
		q.AlbumID = a.ID
	}
	emit := c.printPhoto
	if nameSet {
		needle := strings.ToLower(name)
		emit = func(p *xiaomidrive.Photo) error {
			if strings.Contains(strings.ToLower(p.Name), needle) {
				return c.printPhoto(p)
			}
			return nil
		}
	}
	if err := c.g.WalkPhotos(ctx, q, emit); err != nil {
		return err
	}
	// The overview leaves out the private album; a name search should not.
	if nameSet && album == "" {
		q.AlbumID = xiaomidrive.AlbumPrivate
		return c.g.WalkPhotos(ctx, q, emit)
	}
	return nil
}

func (c *galleryCmd) printPhoto(p *xiaomidrive.Photo) error {
	kind := "img"
	if p.IsVideo() {
		kind = "vid"
	}
	fmt.Fprintf(c.out, "%-18s %s %s %10s  %s\n", p.ID, kind, p.Taken.In(xiaomidrive.GalleryZone).Format("2006-01-02 15:04"), humanBytes(p.Size), p.Name)
	return nil
}

// searchKindNames labels suggestion kinds for display.
var searchKindNames = map[string]string{
	xiaomidrive.SearchKindTag:      "分类",
	xiaomidrive.SearchKindLocation: "地点",
	xiaomidrive.SearchKindTime:     "时间",
	xiaomidrive.SearchKindAlbum:    "相册",
	xiaomidrive.SearchKindPeople:   "人物",
}

// defaultSearchLimit is how many photos a search shows unless --limit says
// otherwise.
const defaultSearchLimit = 100

// errSearchLimit stops WalkSearch once enough photos were printed.
var errSearchLimit = errors.New("search limit reached")

// searchProgressEvery is how often (in photos) progress is shown while a
// sorted search fetches everything; one service page is 100.
const searchProgressEvery = 500

func (c *galleryCmd) search(ctx context.Context, args []string) error {
	var query string
	pick, limit := 0, defaultSearchLimit
	unsorted := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pick":
			i++
			n, err := strconv.Atoi(arg(args, i))
			if err != nil || n < 1 {
				return errors.New("--pick 需要一个从 1 开始的编号")
			}
			pick = n
		case "--limit":
			i++
			n, err := strconv.Atoi(arg(args, i))
			if err != nil || n < 0 {
				return errors.New("--limit 需要一个不小于 0 的整数(0 表示全部)")
			}
			limit = n
		case "--unsorted":
			unsorted = true
		default:
			if query != "" {
				query += " "
			}
			query += args[i]
		}
	}
	if strings.TrimSpace(query) == "" {
		return errors.New("用法: micloud gallery search <关键词> [--pick 编号] [--limit 张数] [--unsorted]")
	}

	suggestions, err := c.g.Suggest(ctx, query)
	if err != nil {
		return err
	}
	if len(suggestions) == 0 {
		fmt.Fprintln(c.out, "没有搜索结果。服务端搜索只认分类、地点、时间和相册名,不认文件名和图片里的文字;按文件名找请用 micloud gallery ls --name。")
		return nil
	}
	for i, s := range suggestions {
		kind := searchKindNames[s.Kind]
		if kind == "" {
			kind = s.Kind
		}
		count := ""
		if s.Count > 0 {
			count = fmt.Sprintf("(%d)", s.Count)
		}
		fmt.Fprintf(c.out, "%2d. [%s] %s%s\n", i+1, kind, s.Title, count)
	}

	if pick == 0 {
		if !c.interactive {
			fmt.Fprintln(c.errOut, "用 --pick <编号> 列出其中一项的照片。")
			return nil
		}
		fmt.Fprint(c.errOut, "选择编号(直接回车退出): ")
		line, err := readLineFrom(c.in)
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 1 {
			return fmt.Errorf("编号 %q 无效", line)
		}
		pick = n
	}
	if pick > len(suggestions) {
		return fmt.Errorf("编号 %d 超出范围(共 %d 项)", pick, len(suggestions))
	}

	chosen := suggestions[pick-1]
	fmt.Fprintf(c.out, "\n%s:\n", chosen.Title)
	if unsorted {
		return c.searchUnsorted(ctx, chosen, limit)
	}
	return c.searchSorted(ctx, chosen, limit)
}

// searchUnsorted prints photos in the service's order as they arrive and
// stops fetching once limit photos are shown.
func (c *galleryCmd) searchUnsorted(ctx context.Context, s *xiaomidrive.SearchSuggestion, limit int) error {
	n := 0
	err := c.g.WalkSearch(ctx, s, func(p *xiaomidrive.Photo) error {
		if limit > 0 && n >= limit {
			return errSearchLimit
		}
		n++
		return c.printPhoto(p)
	})
	switch {
	case errors.Is(err, errSearchLimit):
		fmt.Fprintf(c.errOut, "已显示 %d 张(服务端顺序),用 --limit 调大或 --limit 0 显示全部。\n", limit)
	case err != nil:
		return err
	case n == 0:
		fmt.Fprintln(c.out, "(没有照片)")
	}
	return nil
}

// searchSorted fetches every photo (the service's order is not by date),
// sorts newest first by the date the library files them under, and prints
// the first limit.
func (c *galleryCmd) searchSorted(ctx context.Context, s *xiaomidrive.SearchSuggestion, limit int) error {
	var photos []*xiaomidrive.Photo
	progress := false
	err := c.g.WalkSearch(ctx, s, func(p *xiaomidrive.Photo) error {
		photos = append(photos, p)
		if len(photos)%searchProgressEvery == 0 {
			progress = true
			fmt.Fprintf(c.errOut, "\r正在获取… %d 张", len(photos))
		}
		return nil
	})
	if progress {
		fmt.Fprintln(c.errOut)
	}
	if err != nil {
		return err
	}
	if len(photos) == 0 {
		fmt.Fprintln(c.out, "(没有照片)")
		return nil
	}
	// Stable, so photos with the same date keep the service's order.
	sort.SliceStable(photos, func(i, j int) bool { return photos[i].Taken.After(photos[j].Taken) })
	shown := photos
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, p := range shown {
		c.printPhoto(p)
	}
	if len(shown) < len(photos) {
		fmt.Fprintf(c.errOut, "共 %d 张,已显示最新的 %d 张,用 --limit 调大或 --limit 0 显示全部。\n", len(photos), len(shown))
	}
	return nil
}

func (c *galleryCmd) get(ctx context.Context, args []string) error {
	var id, out string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			i++
			out = arg(args, i)
		default:
			if id == "" {
				id = args[i]
			}
		}
	}
	if id == "" {
		return errors.New("用法: micloud gallery get <照片ID> [-o 输出]")
	}
	if out == "" {
		out = id
	}
	rc, err := c.g.Open(ctx, id)
	if err != nil {
		return err
	}
	defer rc.Close()
	dst, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	n, err := io.Copy(dst, rc)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(out)
		return err
	}
	fmt.Fprintf(c.out, "已下载 %s(%s)-> %s\n", id, humanBytes(n), out)
	return nil
}

func (c *galleryCmd) put(ctx context.Context, args []string) error {
	var local, album, name, date string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			name = arg(args, i)
		case "--date":
			i++
			date = arg(args, i)
		default:
			if local == "" {
				local = args[i]
			} else if album == "" {
				album = args[i]
			} else {
				return fmt.Errorf("多余的参数 %q", args[i])
			}
		}
	}
	if local == "" || album == "" {
		return errors.New(`用法: micloud gallery put <本地文件> <相册> [--name 名称] [--date "YYYY-MM-DD HH:MM:SS"]`)
	}
	fi, err := os.Stat(local)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return errors.New("暂不支持上传目录")
	}
	opts := xiaomidrive.GalleryUploadOptions{ModTime: fi.ModTime()}
	if date != "" {
		if opts.DateTaken, err = parseDateTime(date); err != nil {
			return err
		}
	}
	a, err := c.resolveAlbum(ctx, album)
	if err != nil {
		return err
	}
	if name == "" {
		name = filepath.Base(local)
	}
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()

	size := fi.Size()
	printed := false
	if size > 0 {
		last := -1
		opts.Progress = func(uploaded int64) {
			pct := int(float64(uploaded) / float64(size) * 100)
			if pct != last {
				last = pct
				printed = true
				fmt.Fprintf(c.errOut, "\r上传中… %d%%", pct)
			}
		}
	}
	p, err := c.g.UploadReaderAt(ctx, a.ID, name, src, size, opts)
	if printed {
		fmt.Fprintln(c.errOut)
	}
	if err != nil {
		if errors.Is(err, xiaomidrive.ErrEmptyFile) {
			return errors.New("云相册不接受空文件")
		}
		return err
	}
	note := ""
	if !strings.EqualFold(p.Name, name) {
		note = "(相册里已有同名文件,服务端改了名)"
	}
	fmt.Fprintf(c.out, "已上传 %s -> %s/%s(id %s,归档日期 %s)%s\n", local, a.Name, p.Name, p.ID,
		p.Taken.In(xiaomidrive.GalleryZone).Format("2006-01-02"), note)
	return nil
}

func (c *galleryCmd) mkalbum(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: micloud gallery mkalbum <名称>")
	}
	a, err := c.g.CreateAlbum(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "相册 %s(id %s)\n", a.Name, a.ID)
	return nil
}

func (c *galleryCmd) renameAlbum(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("用法: micloud gallery rename-album <相册> <新名称>")
	}
	a, err := c.resolveAlbum(ctx, args[0])
	if err != nil {
		return err
	}
	na, err := c.g.RenameAlbum(ctx, a.ID, args[1])
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "已重命名为 %s\n", na.Name)
	return nil
}

func (c *galleryCmd) rmalbum(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: micloud gallery rmalbum <相册>")
	}
	a, err := c.resolveAlbum(ctx, args[0])
	if err != nil {
		return err
	}
	if err := c.g.DeleteAlbum(ctx, a.ID); err != nil {
		if errors.Is(err, xiaomidrive.ErrAlbumNotEmpty) {
			return fmt.Errorf("相册 %s 不是空的,只能删除空相册", a.Name)
		}
		return err
	}
	fmt.Fprintf(c.out, "已删除相册 %s\n", a.Name)
	return nil
}

func (c *galleryCmd) mvcp(ctx context.Context, op string, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("用法: micloud gallery %s <照片ID> <相册>", op)
	}
	a, err := c.resolveAlbum(ctx, args[1])
	if err != nil {
		return err
	}
	var p *xiaomidrive.Photo
	verb := "复制"
	if op == "mv" {
		verb = "移动"
		p, err = c.g.Move(ctx, args[0], a.ID)
	} else {
		p, err = c.g.Copy(ctx, args[0], a.ID)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "已%s到 %s,新 id %s\n", verb, a.Name, p.ID)
	return nil
}

func (c *galleryCmd) rm(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: micloud gallery rm <照片ID>")
	}
	if err := c.g.Delete(ctx, args[0]); err != nil {
		return err
	}
	fmt.Fprintln(c.out, "已删除(在相册回收站里)。")
	return nil
}

func (c *galleryCmd) trash(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("用法: micloud gallery trash ls|restore <照片ID>|purge <照片ID>")
	}
	switch args[0] {
	case "ls", "list":
		items, err := c.g.Trash(ctx)
		if err != nil {
			return err
		}
		for _, p := range items {
			fmt.Fprintf(c.out, "%-18s %10s  删除于 %s  %s\n", p.ID, humanBytes(p.Size), p.Deleted.In(xiaomidrive.GalleryZone).Format("2006-01-02 15:04"), p.Name)
		}
		return nil
	case "restore":
		if len(args) < 2 {
			return errors.New("用法: micloud gallery trash restore <照片ID>")
		}
		items, err := c.g.Trash(ctx)
		if err != nil {
			return err
		}
		for _, p := range items {
			if p.ID == args[1] {
				if _, err := c.g.Restore(ctx, p); err != nil {
					return err
				}
				fmt.Fprintln(c.out, "已恢复到原相册。")
				return nil
			}
		}
		return fmt.Errorf("%w: 回收站里没有 %s", xiaomidrive.ErrNotFound, args[1])
	case "purge":
		if len(args) < 2 {
			return errors.New("用法: micloud gallery trash purge <照片ID>")
		}
		if err := c.g.Purge(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintln(c.out, "已永久删除。")
		return nil
	default:
		return fmt.Errorf("未知子命令 %q", args[0])
	}
}

// ---------------------------------------------------------------------------
// backup
// ---------------------------------------------------------------------------

// safeName makes a service-provided name usable as one path component, so a
// name like "../x" cannot escape the backup directory.
func safeName(name, fallback string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\':
			return '_'
		case 0:
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return fallback
	}
	return name
}

type backupItem struct {
	photo *xiaomidrive.Photo
	path  string
}

// planBackup assigns every photo a local path dir/<album>/<name>. Albums
// missing from albumNames use their ID. Names are unique per album on the
// service only case-insensitively, so a path that collides (ignoring case)
// with an earlier one gets the photo ID appended before the extension.
func planBackup(dir string, photos []*xiaomidrive.Photo, albumNames map[string]string) []backupItem {
	items := make([]backupItem, 0, len(photos))
	used := map[string]bool{}
	for _, p := range photos {
		sub := safeName(albumNames[p.AlbumID], safeName(p.AlbumID, "_"))
		name := safeName(p.Name, p.ID)
		rel := filepath.Join(sub, name)
		if used[strings.ToLower(rel)] {
			ext := filepath.Ext(name)
			rel = filepath.Join(sub, strings.TrimSuffix(name, ext)+"_"+p.ID+ext)
		}
		used[strings.ToLower(rel)] = true
		items = append(items, backupItem{photo: p, path: filepath.Join(dir, rel)})
	}
	return items
}

func (c *galleryCmd) backup(ctx context.Context, args []string) error {
	var dir, album string
	skipPrivate := false
	jobs := 1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--album":
			i++
			album = arg(args, i)
		case "--skip-private":
			skipPrivate = true
		case "-j", "--jobs":
			i++
			n, err := strconv.Atoi(arg(args, i))
			if err != nil || n < 1 || n > 8 {
				return errors.New("-j 取 1 到 8")
			}
			jobs = n
		default:
			if dir != "" {
				return fmt.Errorf("多余的参数 %q", args[i])
			}
			dir = args[i]
		}
	}
	if dir == "" {
		return errors.New("用法: micloud gallery backup <目录> [--album 相册] [--skip-private] [-j 并发]")
	}

	albums, err := c.g.Albums(ctx)
	if err != nil {
		return err
	}
	albumNames := map[string]string{}
	for id, name := range systemAlbumNames {
		albumNames[id] = name
	}
	for _, a := range albums {
		albumNames[a.ID] = a.Name
	}

	var photos []*xiaomidrive.Photo
	collect := func(p *xiaomidrive.Photo) error {
		photos = append(photos, p)
		if len(photos)%500 == 0 {
			fmt.Fprintf(c.errOut, "\r正在列出… %d", len(photos))
		}
		return nil
	}
	switch {
	case album != "":
		a, err := c.resolveAlbum(ctx, album)
		if err != nil {
			return err
		}
		if err := c.g.WalkPhotos(ctx, xiaomidrive.PhotoQuery{AlbumID: a.ID}, collect); err != nil {
			return err
		}
	case skipPrivate:
		if err := c.g.WalkPhotos(ctx, xiaomidrive.PhotoQuery{}, collect); err != nil {
			return err
		}
	default:
		if err := c.g.WalkLibrary(ctx, collect); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.errOut, "\r共 %d 项。\n", len(photos))

	items := planBackup(dir, photos, albumNames)

	var done, skipped, failed atomic.Int64
	var blocked atomic.Bool
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan backupItem)
	var wg sync.WaitGroup
	var outMu sync.Mutex
	report := func(format string, a ...any) {
		outMu.Lock()
		defer outMu.Unlock()
		fmt.Fprintf(c.errOut, format, a...)
	}
	total := len(items)
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range work {
				// Once stopped (blocked or interrupted), drain without work:
				// the feeder's select may still hand over an item.
				if wctx.Err() != nil {
					continue
				}
				if fi, err := os.Stat(it.path); err == nil && fi.Size() == it.photo.Size {
					skipped.Add(1)
					continue
				}
				err := c.backupOne(wctx, it)
				n := done.Load() + skipped.Load() + failed.Load() + 1
				switch {
				case err == nil:
					done.Add(1)
					report("[%d/%d] %s\n", n, total, it.path)
				case errors.Is(err, xiaomidrive.ErrBlocked):
					failed.Add(1)
					if !blocked.Swap(true) {
						report("小米暂时拒绝了相册请求,停止备份。过一段时间重新运行即可,已下载的会跳过。\n")
					}
					cancel()
				case wctx.Err() != nil:
					// Interrupted or stopped after a block; not a failure
					// of this item.
				default:
					failed.Add(1)
					report("[%d/%d] 失败 %s: %v\n", n, total, it.path, err)
				}
			}
		}()
	}
feed:
	for _, it := range items {
		select {
		case work <- it:
		case <-wctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()

	fmt.Fprintf(c.out, "下载 %d,跳过(已存在)%d,失败 %d。\n", done.Load(), skipped.Load(), failed.Load())
	if blocked.Load() {
		return xiaomidrive.ErrBlocked
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if failed.Load() > 0 {
		return fmt.Errorf("%d 项失败", failed.Load())
	}
	return nil
}

// backupOne downloads one photo to a temp file next to its target, checks size
// and SHA-1, then moves it into place and dates it with the photo's date.
func (c *galleryCmd) backupOne(ctx context.Context, it backupItem) error {
	if err := os.MkdirAll(filepath.Dir(it.path), 0o755); err != nil {
		return err
	}
	tmp := it.path + ".part"
	rc, err := c.g.Open(ctx, it.photo.ID)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	h := sha1.New()
	n, err := io.Copy(io.MultiWriter(f, h), rc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != it.photo.Size {
		err = fmt.Errorf("大小 %d,应为 %d", n, it.photo.Size)
	}
	if err == nil && it.photo.SHA1 != "" && hex.EncodeToString(h.Sum(nil)) != it.photo.SHA1 {
		err = errors.New("SHA-1 与记录不一致")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, it.path); err != nil {
		os.Remove(tmp)
		return err
	}
	if !it.photo.Taken.IsZero() {
		_ = os.Chtimes(it.path, it.photo.Taken, it.photo.Taken)
	}
	return nil
}
