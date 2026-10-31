package xiaomidrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// System album IDs. The overview ("all photos") is not an album.
const (
	AlbumCamera      = "1"
	AlbumScreenshots = "2"
	// AlbumPrivate is the private album. Its photos are NOT part of the
	// overview or the timeline and must be listed with this ID explicitly.
	AlbumPrivate = "1000"
)

// GalleryZone is the time zone the service uses to bucket photos into days
// (UTC+8), both for Timeline keys and for PhotoQuery date ranges.
var GalleryZone = time.FixedZone("UTC+8", 8*3600)

// galleryPageSize is the page size for listing photos. 200 was verified over a
// full 9000-item scan with no duplicates or gaps; 500 also works.
const galleryPageSize = 200

// Gallery is the cloud photo library (云相册) of the logged-in account. Get it
// with Client.Gallery. It shares the client's session and is safe for
// concurrent use.
//
// Photo IDs are not stable: moving a photo gives it a new ID, and copies get
// new IDs. Use the returned records, and SHA1 plus name as a secondary key.
type Gallery struct {
	c *Client

	mu    sync.Mutex
	ready bool
}

// Gallery returns the client's photo library.
func (c *Client) Gallery() *Gallery {
	c.galleryOnce.Do(func() { c.gallery = &Gallery{c: c} })
	return c.gallery
}

// ensure runs the one-time startup the web client does before using the
// gallery: refuse end-to-end encrypted accounts, then call prepare. A failure
// is not remembered, so the next call tries again.
func (g *Gallery) ensure(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ready {
		return nil
	}
	if err := g.checkE2EE(ctx); err != nil {
		return err
	}
	if _, err := g.c.s.postForm(ctx, "/gallery/user/lite/index/prepare", nil); err != nil {
		return err
	}
	g.ready = true
	return nil
}

// checkE2EE refuses accounts with gallery end-to-end encryption, whose content
// this library would download as ciphertext. An account without it reports
// e2eeStatus "close"; any other non-empty value is treated as encrypted. If
// the status endpoint itself fails (other than for auth), the check is
// skipped rather than blocking the gallery.
func (g *Gallery) checkE2EE(ctx context.Context) error {
	q := url.Values{"hsid": {"2"}, "appId": {"micloud"}}
	raw, err := g.c.s.get(ctx, "/mic/keybag/v1/getEncInfo", q)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) || ctx.Err() != nil {
			return err
		}
		g.c.s.logf("xiaomidrive: gallery: e2ee status check skipped: %v", err)
		return nil
	}
	var d struct {
		E2EEStatus string `json:"e2eeStatus"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		g.c.s.logf("xiaomidrive: gallery: e2ee status decode: %v", err)
		return nil
	}
	if d.E2EEStatus != "" && d.E2EEStatus != "close" {
		return fmt.Errorf("%w (e2eeStatus %q)", ErrE2EEUnsupported, d.E2EEStatus)
	}
	return nil
}

// MediaType is image or video.
type MediaType string

const (
	MediaImage MediaType = "image"
	MediaVideo MediaType = "video"
)

// Photo is a photo or video record.
type Photo struct {
	ID      string
	AlbumID string
	// Name is the file name. On upload the service may rename a same-name
	// file to "<name>_<ms><ext>"; names are case-insensitive per album.
	Name     string
	Title    string
	Type     MediaType
	MimeType string
	Size     int64
	// SHA1 of the original bytes, lower-case hex. The service stores what
	// the uploader declared without checking it.
	SHA1 string
	// Status is "custom" (normal), "deleted" (in the trash), "purged" or
	// "temp" (created but never committed).
	Status string
	// Tag is the sync cursor; it changes on every modification.
	Tag string
	// Taken is the date the library sorts and buckets the photo by.
	Taken    time.Time
	Modified time.Time
	Created  time.Time
	// Deleted is set for trash entries.
	Deleted time.Time
	// Duration is set for videos (whole seconds).
	Duration      time.Duration
	Width, Height int
	IsLivePhoto   bool
	IsFavorite    bool
	// Thumbnail (270×270) and BigThumbnail (1080×1080) are signed,
	// cookie-free URLs valid for about a day. Their size cannot be changed.
	Thumbnail    string
	BigThumbnail string
}

// IsVideo reports whether p is a video.
func (p *Photo) IsVideo() bool { return p.Type == MediaVideo }

type rawThumb struct {
	Data  string   `json:"data"`
	IsURL flexBool `json:"isUrl"`
}

type rawPhoto struct {
	ID               flexString      `json:"id"`
	GroupID          flexString      `json:"groupId"`
	FileName         string          `json:"fileName"`
	Title            string          `json:"title"`
	Type             string          `json:"type"`
	MimeType         string          `json:"mimeType"`
	SHA1             string          `json:"sha1"`
	Size             flexInt         `json:"size"`
	Status           string          `json:"status"`
	Tag              flexString      `json:"tag"`
	SortTime         flexInt         `json:"sortTime"`
	DateTaken        flexInt         `json:"dateTaken"`
	DateModified     flexInt         `json:"dateModified"`
	CreateTime       flexInt         `json:"createTime"`
	DeleteTime       flexInt         `json:"deleteTime"`
	Duration         flexInt         `json:"duration"`
	IsLivePhoto      flexBool        `json:"isLivePhoto"`
	IsFavorite       flexBool        `json:"isFavorite"`
	ThumbnailInfo    *rawThumb       `json:"thumbnailInfo"`
	BigThumbnailInfo *rawThumb       `json:"bigThumbnailInfo"`
	ExifInfo         json.RawMessage `json:"exifInfo"`
}

func (r *rawPhoto) toPhoto() *Photo {
	p := &Photo{
		ID:          string(r.ID),
		AlbumID:     string(r.GroupID),
		Name:        r.FileName,
		Title:       r.Title,
		Type:        MediaType(r.Type),
		MimeType:    r.MimeType,
		Size:        int64(r.Size),
		SHA1:        r.SHA1,
		Status:      r.Status,
		Tag:         string(r.Tag),
		Taken:       msTime(r.DateTaken),
		Modified:    msTime(r.DateModified),
		Created:     msTime(r.CreateTime),
		Deleted:     msTime(r.DeleteTime),
		Duration:    time.Duration(r.Duration) * time.Second,
		IsLivePhoto: bool(r.IsLivePhoto),
		IsFavorite:  bool(r.IsFavorite),
	}
	if p.Taken.IsZero() {
		p.Taken = msTime(r.SortTime)
	}
	if r.ThumbnailInfo != nil && bool(r.ThumbnailInfo.IsURL) {
		p.Thumbnail = r.ThumbnailInfo.Data
	}
	if r.BigThumbnailInfo != nil && bool(r.BigThumbnailInfo.IsURL) {
		p.BigThumbnail = r.BigThumbnailInfo.Data
	}
	// exifInfo is server-filled and loosely typed; a shape we do not expect
	// must not break listing, so it is decoded on its own and errors ignored.
	if len(r.ExifInfo) > 0 {
		var ex struct {
			ImageWidth  flexInt `json:"imageWidth"`
			ImageLength flexInt `json:"imageLength"`
		}
		if json.Unmarshal(r.ExifInfo, &ex) == nil {
			p.Width, p.Height = int(ex.ImageWidth), int(ex.ImageLength)
		}
	}
	return p
}

// Album is a gallery album (group).
type Album struct {
	ID         string
	Name       string
	MediaCount int
	Status     string
	// Thumbnail is a signed cover URL, valid for about a day. Empty if the
	// album has no photos.
	Thumbnail string
	Updated   time.Time
}

type rawAlbum struct {
	AlbumID        flexString `json:"albumId"`
	ID             flexString `json:"id"`
	Name           string     `json:"name"`
	FileName       string     `json:"fileName"`
	MediaCount     flexInt    `json:"mediaCount"`
	Status         string     `json:"status"`
	LastUpdateTime flexInt    `json:"lastUpdateTime"`
	DateModified   flexInt    `json:"dateModified"`
	Thumbnails     []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

func (r *rawAlbum) toAlbum() *Album {
	a := &Album{
		ID:         string(r.AlbumID),
		Name:       r.Name,
		MediaCount: int(r.MediaCount),
		Status:     r.Status,
		Updated:    msTime(r.LastUpdateTime),
	}
	if a.ID == "" {
		a.ID = string(r.ID)
	}
	if a.Name == "" {
		a.Name = r.FileName
	}
	if a.Updated.IsZero() {
		a.Updated = msTime(r.DateModified)
	}
	if len(r.Thumbnails) > 0 {
		a.Thumbnail = r.Thumbnails[0].URL
	}
	return a
}

// Albums lists the account's albums, reading every page. Whether the system
// albums (camera, screenshots, private) are included is decided by the
// service.
func (g *Gallery) Albums(ctx context.Context) ([]*Album, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	var out []*Album
	for page := 0; ; page++ {
		q := url.Values{
			"pageNum":         {strconv.Itoa(page)},
			"pageSize":        {"50"},
			"isShared":        {"false"},
			"numOfThumbnails": {"1"},
		}
		raw, err := g.c.s.get(ctx, "/gallery/user/album/list", q)
		if err != nil {
			return nil, err
		}
		var d struct {
			Albums     []rawAlbum `json:"albums"`
			IsLastPage flexBool   `json:"isLastPage"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("xiaomidrive: gallery albums decode: %w", err)
		}
		for i := range d.Albums {
			out = append(out, d.Albums[i].toAlbum())
		}
		if bool(d.IsLastPage) || len(d.Albums) == 0 {
			break
		}
	}
	return out, nil
}

// VideoCount returns the number of videos in the library (the web client's
// "all videos" view).
func (g *Gallery) VideoCount(ctx context.Context) (int, error) {
	if err := g.ensure(ctx); err != nil {
		return 0, err
	}
	raw, err := g.c.s.get(ctx, "/gallery/user/album/video", url.Values{"isShared": {"false"}})
	if err != nil {
		return 0, err
	}
	var d struct {
		Album struct {
			MediaCount flexInt `json:"mediaCount"`
		} `json:"album"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return 0, fmt.Errorf("xiaomidrive: gallery video count decode: %w", err)
	}
	return int(d.Album.MediaCount), nil
}

// PhotoQuery selects photos. The zero value is the whole overview, which
// covers every album except AlbumPrivate.
type PhotoQuery struct {
	// AlbumID restricts to one album; empty means the overview.
	AlbumID string
	// Start and End restrict to a range of days, both inclusive. Only the
	// calendar date in GalleryZone counts. Zero means unbounded.
	Start, End time.Time
	// OnlyVideo restricts to videos.
	OnlyVideo bool
}

func (q PhotoQuery) values() url.Values {
	v := url.Values{}
	if q.AlbumID != "" {
		v.Set("albumId", q.AlbumID)
	}
	if q.OnlyVideo {
		v.Set("onlyVideo", "true")
	}
	if !q.Start.IsZero() || !q.End.IsZero() {
		start, end := q.Start, q.End
		if start.IsZero() {
			start = time.UnixMilli(0)
		}
		if end.IsZero() {
			end = time.Now()
		}
		v.Set("startDate", galleryDay(start))
		v.Set("endDate", galleryDay(end))
	}
	return v
}

// galleryDay formats t as the service's YYYYMMDD day key.
func galleryDay(t time.Time) string { return t.In(GalleryZone).Format("20060102") }

// Timeline returns the number of photos per day ("YYYYMMDD" in GalleryZone)
// for the overview or one album. Only AlbumID and OnlyVideo of q are used.
func (g *Gallery) Timeline(ctx context.Context, q PhotoQuery) (map[string]int, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	v := url.Values{}
	if q.AlbumID != "" {
		v.Set("albumId", q.AlbumID)
	}
	if q.OnlyVideo {
		v.Set("onlyVideo", "true")
	}
	raw, err := g.c.s.get(ctx, "/gallery/user/timeline", v)
	if err != nil {
		return nil, err
	}
	var d struct {
		DayCount map[string]flexInt `json:"dayCount"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery timeline decode: %w", err)
	}
	out := make(map[string]int, len(d.DayCount))
	for k, n := range d.DayCount {
		out[k] = int(n)
	}
	return out, nil
}

// WalkPhotos calls fn for every photo matching q, newest first, one page at a
// time. Returning an error from fn stops the walk and returns that error.
func (g *Gallery) WalkPhotos(ctx context.Context, q PhotoQuery, fn func(*Photo) error) error {
	if err := g.ensure(ctx); err != nil {
		return err
	}
	base := q.values()
	for page := 0; ; page++ {
		v := url.Values{}
		for k, vs := range base {
			v[k] = vs
		}
		v.Set("pageNum", strconv.Itoa(page))
		v.Set("pageSize", strconv.Itoa(galleryPageSize))
		raw, err := g.c.s.get(ctx, "/gallery/user/galleries", v)
		if err != nil {
			return err
		}
		var d struct {
			Galleries  []rawPhoto `json:"galleries"`
			IsLastPage flexBool   `json:"isLastPage"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return fmt.Errorf("xiaomidrive: gallery list decode: %w", err)
		}
		for i := range d.Galleries {
			if err := fn(d.Galleries[i].toPhoto()); err != nil {
				return err
			}
		}
		if bool(d.IsLastPage) || len(d.Galleries) == 0 {
			return nil
		}
	}
}

// Photos returns every photo matching q, newest first.
func (g *Gallery) Photos(ctx context.Context, q PhotoQuery) ([]*Photo, error) {
	var out []*Photo
	err := g.WalkPhotos(ctx, q, func(p *Photo) error {
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// WalkLibrary calls fn for every photo and video in the account: the overview
// followed by the private album, which the overview leaves out. This is the
// complete set a backup needs.
func (g *Gallery) WalkLibrary(ctx context.Context, fn func(*Photo) error) error {
	if err := g.WalkPhotos(ctx, PhotoQuery{}, fn); err != nil {
		return err
	}
	return g.WalkPhotos(ctx, PhotoQuery{AlbumID: AlbumPrivate}, fn)
}
