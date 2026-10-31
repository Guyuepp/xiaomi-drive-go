package xiaomidrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// validateAlbumName rejects names the service would silently change: it
// strips "/" and surrounding spaces instead of failing.
func validateAlbumName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%w: album name %q", ErrInvalidName, name)
	}
	return nil
}

func decodeAlbumContent(raw json.RawMessage, op string) (*Album, error) {
	var d struct {
		Content rawAlbum `json:"content"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery %s decode: %w", op, err)
	}
	return d.Content.toAlbum(), nil
}

func decodePhotoContent(raw json.RawMessage, key, op string) (*Photo, error) {
	var d map[string]json.RawMessage
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery %s decode: %w", op, err)
	}
	var r rawPhoto
	if body := d[key]; len(body) > 0 {
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("xiaomidrive: gallery %s decode: %w", op, err)
		}
	}
	if r.ID == "" {
		return nil, fmt.Errorf("xiaomidrive: gallery %s: response has no record", op)
	}
	return r.toPhoto(), nil
}

func nowMillis() string { return strconv.FormatInt(time.Now().UnixMilli(), 10) }

// CreateAlbum creates an album. It is idempotent: if an album with this name
// exists, the service returns it instead of creating another.
func (g *Gallery) CreateAlbum(ctx context.Context, name string) (*Album, error) {
	if err := validateAlbumName(name); err != nil {
		return nil, err
	}
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	raw, err := g.c.s.postForm(ctx, "/gallery/album/full", url.Values{"fileName": {name}})
	if err != nil {
		return nil, err
	}
	return decodeAlbumContent(raw, "create album")
}

// RenameAlbum renames an album. What happens when newName is already taken
// has not been verified.
func (g *Gallery) RenameAlbum(ctx context.Context, id, newName string) (*Album, error) {
	if err := validateAlbumName(newName); err != nil {
		return nil, err
	}
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	content, err := json.Marshal(map[string]any{
		"fileName": newName, "id": id, "type": "group", "status": "custom",
		"dateModified": time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	form := url.Values{"content": {string(content)}, "ckey": {nowMillis()}}
	raw, err := g.c.s.postForm(ctx, "/gallery/album/put", form)
	if err != nil {
		return nil, err
	}
	return decodeAlbumContent(raw, "rename album")
}

// DeleteAlbum deletes an empty album. An album that still holds photos yields
// ErrAlbumNotEmpty; a missing album yields ErrNotFound.
func (g *Gallery) DeleteAlbum(ctx context.Context, id string) error {
	if err := g.ensure(ctx); err != nil {
		return err
	}
	_, err := g.c.s.postForm(ctx, "/gallery/album/full/"+url.PathEscape(id)+"/delete", url.Values{"tag": {"0"}})
	return err
}

// Move moves a photo into albumID and returns its new record. The service
// copies it and purges the original, so the photo gets a NEW ID. Repeating a
// move of the same source ID returns the same result.
func (g *Gallery) Move(ctx context.Context, id, albumID string) (*Photo, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	form := url.Values{"tag": {"1"}, "albumId": {albumID}}
	raw, err := g.c.s.postForm(ctx, "/gallery/image/"+url.PathEscape(id)+"/move", form)
	if err != nil {
		return nil, err
	}
	return decodePhotoContent(raw, "copyContent", "move")
}

// Copy copies a photo into albumID without re-uploading and returns the copy,
// which has a new ID. Copying the same photo into the same album again
// returns the earlier copy.
func (g *Gallery) Copy(ctx context.Context, id, albumID string) (*Photo, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	form := url.Values{"tag": {"1"}, "toAlbum": {albumID}}
	raw, err := g.c.s.postForm(ctx, "/gallery/full/"+url.PathEscape(id)+"/copy", form)
	if err != nil {
		return nil, err
	}
	return decodePhotoContent(raw, "content", "copy")
}

// Delete moves a photo to the trash. It is idempotent. A missing ID yields
// ErrNotFound. Purge it afterwards to free space.
func (g *Gallery) Delete(ctx context.Context, id string) error {
	if err := g.ensure(ctx); err != nil {
		return err
	}
	_, err := g.c.s.postForm(ctx, "/gallery/info/delete", url.Values{"id": {id}})
	return err
}

// trashPageLimit is the page size the web trash view uses.
const trashPageLimit = 50

// Trash lists the gallery trash. Paging past the first page follows syncTag
// as the next maxTag; that continuation has not been verified, so the walk
// also stops when a page brings nothing new.
func (g *Gallery) Trash(ctx context.Context) ([]*Photo, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	var out []*Photo
	seen := map[string]bool{}
	maxTag := "0"
	for {
		q := url.Values{"_dc": {nowMillis()}, "maxTag": {maxTag}, "limit": {strconv.Itoa(trashPageLimit)}}
		raw, err := g.get(ctx, "/gallery/deleted_v2", q)
		if err != nil {
			return nil, err
		}
		var d struct {
			LastPage flexBool   `json:"lastPage"`
			SyncTag  flexString `json:"syncTag"`
			Content  []rawPhoto `json:"content"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("xiaomidrive: gallery trash decode: %w", err)
		}
		added := 0
		for i := range d.Content {
			p := d.Content[i].toPhoto()
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			out = append(out, p)
			added++
		}
		next := string(d.SyncTag)
		if bool(d.LastPage) || added == 0 || next == "" || next == maxTag {
			return out, nil
		}
		maxTag = next
	}
}

// Restore moves a trashed photo back into its original album, keeping its ID.
// p should come from Trash (its Tag is sent along). It is idempotent.
func (g *Gallery) Restore(ctx context.Context, p *Photo) (*Photo, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	raw, err := g.c.s.postForm(ctx, "/gallery/info/recovery", url.Values{"id": {p.ID}, "tag": {p.Tag}})
	if err != nil {
		return nil, err
	}
	return decodePhotoContent(raw, "content", "restore")
}

// Purge permanently deletes a photo. There is no "empty trash" call; purge
// items one by one. It is idempotent.
func (g *Gallery) Purge(ctx context.Context, id string) error {
	if err := g.ensure(ctx); err != nil {
		return err
	}
	_, err := g.c.s.postForm(ctx, "/gallery/info/purge", url.Values{"id": {id}, "ckey": {nowMillis()}})
	return err
}
