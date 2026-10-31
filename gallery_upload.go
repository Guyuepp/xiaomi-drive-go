package xiaomidrive

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// GalleryUploadOptions tunes a gallery upload.
type GalleryUploadOptions struct {
	// Concurrency is the maximum number of blocks uploaded at once (default 4).
	Concurrency int
	// MimeType overrides detection by extension. It must be image/* or
	// video/*.
	MimeType string
	// DateTaken is when the photo was taken. It decides which day the
	// library files the photo under. The service ignores the EXIF date in
	// the file, so set this when importing existing photos.
	DateTaken time.Time
	// ModTime is used as the date when DateTaken is zero, typically the
	// local file's modification time. If both are zero the photo is dated
	// now.
	ModTime time.Time
	// Duration of a video. When zero it is read from MP4/MOV headers if
	// possible.
	Duration time.Duration
	// Progress, if set, is called with bytes uploaded so far.
	Progress func(uploaded int64)
	// WrapBody, if set, wraps each block's request body before it is sent.
	WrapBody func(ctx context.Context, r io.Reader) io.Reader
}

// exifDateLayout is the format exifInfo.dateTime must use; the service reads
// it as a wall-clock time in GalleryZone.
const exifDateLayout = "2006:01:02 15:04:05"

// galleryMimeTypes covers media extensions the system mime table may lack.
var galleryMimeTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp",
	".heic": "image/heic", ".heif": "image/heif", ".avif": "image/avif",
	".avci": "image/avci", ".tif": "image/tiff", ".tiff": "image/tiff",
	".dng": "image/x-adobe-dng",
	".mp4": "video/mp4", ".m4v": "video/x-m4v", ".mov": "video/quicktime",
	".3gp": "video/3gpp", ".mkv": "video/x-matroska", ".webm": "video/webm",
	".avi": "video/x-msvideo",
}

// galleryMediaType returns the MIME type and media type for an upload, or
// ErrInvalidArgument if it is not an image or a video.
func galleryMediaType(name, override string) (string, MediaType, error) {
	mt := override
	if mt == "" {
		ext := strings.ToLower(filepath.Ext(name))
		mt = galleryMimeTypes[ext]
		if mt == "" {
			mt = mime.TypeByExtension(ext)
		}
	}
	if base, _, err := mime.ParseMediaType(mt); err == nil {
		mt = base
	}
	switch {
	case strings.HasPrefix(mt, "image/"):
		return mt, MediaImage, nil
	case strings.HasPrefix(mt, "video/"):
		return mt, MediaVideo, nil
	}
	return "", "", fmt.Errorf("%w: %q is not an image or video (type %q)", ErrInvalidArgument, name, mt)
}

// validateGalleryFileName checks what is known to be safe for a photo name.
// The gallery's naming rules are not fully known; these are the obvious ones.
func validateGalleryFileName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

type galleryBlockInfo struct {
	Blob struct{} `json:"blob"`
	Size int64    `json:"size"`
	MD5  string   `json:"md5"`
	SHA1 string   `json:"sha1"`
}

type galleryCreateContent struct {
	Type         MediaType      `json:"type"`
	GroupID      string         `json:"groupId"`
	MimeType     string         `json:"mimeType"`
	FileName     string         `json:"fileName"`
	Title        string         `json:"title"`
	SHA1         string         `json:"sha1"`
	Size         int64          `json:"size"`
	DateModified int64          `json:"dateModified"`
	Duration     int64          `json:"duration,omitempty"`
	ExifInfo     map[string]any `json:"exifInfo,omitempty"`
}

type galleryCreateResp struct {
	FileExists   flexBool `json:"fileExists"`
	IsDuplicated flexBool `json:"isDuplicated"`
	KSS          *struct {
		Stat       string         `json:"stat"`
		NodeURLs   []string       `json:"node_urls"`
		FileMeta   string         `json:"file_meta"`
		BlockMetas []kssBlockMeta `json:"block_metas"`
	} `json:"kss"`
	Content rawPhoto `json:"content"`
}

// Upload stores content as a photo or video in albumID. Every upload needs an
// album; there is no upload into the overview alone.
//
// Content the service already has (same SHA-1) is a rapid upload. Re-uploading
// the same name and content into the same album returns the existing photo. A
// same-name upload with different content is kept alongside and renamed by the
// service to "<name>_<ms><ext>"; check the returned Photo.Name.
//
// If committing fails after the create step, a hidden "temp" record is left on
// the service that cannot be deleted; hashing and validation therefore happen
// before anything is sent. Empty files are rejected with ErrEmptyFile.
func (g *Gallery) Upload(ctx context.Context, albumID, name string, r io.Reader, opts GalleryUploadOptions) (*Photo, error) {
	if err := validateGalleryFileName(name); err != nil {
		return nil, err
	}
	if _, _, err := galleryMediaType(name, opts.MimeType); err != nil {
		return nil, err
	}
	sp, err := spoolContent(r)
	if err != nil {
		return nil, err
	}
	defer sp.cleanup()
	return g.uploadSpooled(ctx, albumID, name, sp, opts)
}

// UploadReaderAt is Upload for random-access content of a known size. The
// content is read twice (hash, then send) but not copied.
func (g *Gallery) UploadReaderAt(ctx context.Context, albumID, name string, ra io.ReaderAt, size int64, opts GalleryUploadOptions) (*Photo, error) {
	if err := validateGalleryFileName(name); err != nil {
		return nil, err
	}
	if _, _, err := galleryMediaType(name, opts.MimeType); err != nil {
		return nil, err
	}
	if size <= 0 {
		return nil, ErrEmptyFile
	}
	sp, err := hashReaderAt(ra, size)
	if err != nil {
		return nil, err
	}
	return g.uploadSpooled(ctx, albumID, name, sp, opts)
}

func (g *Gallery) uploadSpooled(ctx context.Context, albumID, name string, sp *spooled, opts GalleryUploadOptions) (*Photo, error) {
	if sp.size == 0 {
		return nil, ErrEmptyFile
	}
	if albumID == "" {
		return nil, fmt.Errorf("%w: an album is required", ErrInvalidArgument)
	}
	mimeType, mediaType, err := galleryMediaType(name, opts.MimeType)
	if err != nil {
		return nil, err
	}
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}

	content := galleryCreateContent{
		Type: mediaType, GroupID: albumID, MimeType: mimeType,
		FileName: name, Title: strings.TrimSuffix(name, filepath.Ext(name)),
		SHA1: sp.sha1, Size: sp.size,
	}
	switch {
	case !opts.DateTaken.IsZero():
		// dateModified alone is enough; exifInfo.dateTime takes precedence
		// on the service and is set too, as the notes recommend.
		content.DateModified = opts.DateTaken.UnixMilli()
		content.ExifInfo = map[string]any{"dateTime": opts.DateTaken.In(GalleryZone).Format(exifDateLayout)}
	case !opts.ModTime.IsZero():
		content.DateModified = opts.ModTime.UnixMilli()
	default:
		content.DateModified = time.Now().UnixMilli()
	}
	if mediaType == MediaVideo {
		d := opts.Duration
		if d <= 0 {
			d, _ = mp4Duration(sp.ra, sp.size)
		}
		if d > 0 {
			content.Duration = int64(math.Round(d.Seconds()))
		}
	}

	blocks := make([]galleryBlockInfo, len(sp.blocks))
	for i, b := range sp.blocks {
		blocks[i] = galleryBlockInfo{Size: b.size, MD5: b.md5, SHA1: b.sha1}
	}
	dataJSON, err := json.Marshal(map[string]any{"content": content, "block_infos": blocks})
	if err != nil {
		return nil, err
	}
	form := url.Values{"data": {string(dataJSON)}, "isClientUploadThumbnail": {"false"}}
	raw, err := g.c.s.postForm(ctx, "/gallery/user/full", form)
	if err != nil {
		return nil, err
	}
	var cr galleryCreateResp
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery upload create decode: %w", err)
	}
	if bool(cr.FileExists) {
		if cr.Content.ID == "" {
			return nil, fmt.Errorf("xiaomidrive: gallery upload: rapid upload reported no id")
		}
		if opts.Progress != nil {
			opts.Progress(sp.size)
		}
		return cr.Content.toPhoto(), nil
	}
	photoID := string(cr.Content.ID)
	if cr.KSS == nil || cr.KSS.Stat != "OK" || len(cr.KSS.NodeURLs) == 0 || cr.KSS.FileMeta == "" || photoID == "" {
		return nil, fmt.Errorf("xiaomidrive: gallery upload: incomplete create response")
	}

	commitMetas, err := g.c.uploadBlocks(ctx, cr.KSS.NodeURLs[0], cr.KSS.FileMeta, cr.KSS.BlockMetas, sp, UploadOptions{
		Concurrency: opts.Concurrency, Progress: opts.Progress, WrapBody: opts.WrapBody,
	})
	if err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery upload of %q (temp record %s left behind): %w", name, photoID, err)
	}

	// Commit metas must be in block order, or the service answers 10001.
	type commitMeta struct {
		CommitMeta string `json:"commit_meta"`
	}
	cms := make([]commitMeta, len(commitMetas))
	for i, m := range commitMetas {
		cms[i] = commitMeta{CommitMeta: m}
	}
	commitJSON, err := json.Marshal(map[string]any{"kss": map[string]any{
		"file_meta": cr.KSS.FileMeta, "commit_metas": cms,
	}})
	if err != nil {
		return nil, err
	}
	raw, err = g.c.s.postForm(ctx, "/gallery/user/full/"+url.PathEscape(photoID)+"/storage", url.Values{"data": {string(commitJSON)}})
	if err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery upload commit of %q (temp record %s left behind): %w", name, photoID, err)
	}
	var done struct {
		Content rawPhoto `json:"content"`
	}
	if err := json.Unmarshal(raw, &done); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery upload commit decode: %w", err)
	}
	if done.Content.ID == "" {
		done.Content.ID = flexString(photoID)
	}
	return done.Content.toPhoto(), nil
}

// mp4Duration reads the duration from the moov/mvhd box of an MP4 or
// QuickTime file. It returns 0 and an error if the box cannot be found.
func mp4Duration(ra io.ReaderAt, size int64) (time.Duration, error) {
	moovOff, moovSize, err := findBox(ra, 0, size, "moov")
	if err != nil {
		return 0, err
	}
	mvhdOff, mvhdSize, err := findBox(ra, moovOff, moovOff+moovSize, "mvhd")
	if err != nil {
		return 0, err
	}
	// Box payload: version(1) flags(3), then v0: ctime(4) mtime(4)
	// timescale(4) duration(4); v1: ctime(8) mtime(8) timescale(4) duration(8).
	var hdr [32]byte
	n := int64(len(hdr))
	if mvhdSize < n {
		n = mvhdSize
	}
	if _, err := ra.ReadAt(hdr[:n], mvhdOff); err != nil && err != io.EOF {
		return 0, err
	}
	var timescale, duration uint64
	switch {
	case hdr[0] == 0 && n >= 20:
		timescale = uint64(binary.BigEndian.Uint32(hdr[12:16]))
		duration = uint64(binary.BigEndian.Uint32(hdr[16:20]))
	case hdr[0] == 1 && n >= 32:
		timescale = uint64(binary.BigEndian.Uint32(hdr[20:24]))
		duration = binary.BigEndian.Uint64(hdr[24:32])
	default:
		return 0, fmt.Errorf("xiaomidrive: mvhd: unsupported version %d", hdr[0])
	}
	if timescale == 0 {
		return 0, fmt.Errorf("xiaomidrive: mvhd: zero timescale")
	}
	return time.Duration(float64(duration) / float64(timescale) * float64(time.Second)), nil
}

// findBox scans the boxes in [start, end) for typ and returns the offset and
// size of its payload.
func findBox(ra io.ReaderAt, start, end int64, typ string) (int64, int64, error) {
	var hdr [16]byte
	for off := start; off+8 <= end; {
		if _, err := ra.ReadAt(hdr[:8], off); err != nil {
			return 0, 0, err
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		headLen := int64(8)
		switch boxSize {
		case 1:
			if _, err := ra.ReadAt(hdr[8:16], off+8); err != nil {
				return 0, 0, err
			}
			boxSize = int64(binary.BigEndian.Uint64(hdr[8:16]))
			headLen = 16
		case 0:
			boxSize = end - off
		}
		if boxSize < headLen || off+boxSize > end {
			return 0, 0, fmt.Errorf("xiaomidrive: malformed box at %d", off)
		}
		if string(hdr[4:8]) == typ {
			return off + headLen, boxSize - headLen, nil
		}
		off += boxSize
	}
	return 0, 0, fmt.Errorf("xiaomidrive: box %q not found", typ)
}
