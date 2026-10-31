// Package driver adapts the micloud core client to an OpenList-style driver.
//
// It mirrors the shapes of OpenList's internal/driver and internal/model
// (Obj, Link, and the Reader/Mkdir/Move/Rename/Copy/Remove/Put capability
// methods) using only the core library, so it builds standalone. To wire it
// into a real OpenList tree, copy this file into drivers/micloud/, swap the
// local Obj/Link types for model.Obj/model.Link, and add the Additional/Config
// boilerplate; the method bodies forward to *micloud.Client unchanged.
//
// Files are identified by their service ID: every returned Obj carries the real
// file ID in GetID(), which the driver passes straight back to the client.
package driver

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/Guyuepp/micloud"
)

// Obj is the subset of OpenList's model.Obj that this driver produces and
// consumes. *micloud.File satisfies it.
type Obj interface {
	GetID() string
	GetName() string
	GetSize() int64
	ModTime() time.Time
	CreateTime() time.Time
	IsDir() bool
	GetPath() string
}

// Link mirrors OpenList's model.Link for a direct download URL.
type Link struct {
	URL        string
	Header     http.Header
	Expiration *time.Duration
}

// Driver is an OpenList-style driver backed by a micloud.Client.
type Driver struct {
	Client *micloud.Client
	// RootID is the folder the mount is rooted at (defaults to the drive root).
	RootID string
}

// New builds a Driver rooted at the drive root.
func New(c *micloud.Client) *Driver {
	return &Driver{Client: c, RootID: micloud.RootID}
}

func (d *Driver) rootID() string {
	if d.RootID == "" {
		return micloud.RootID
	}
	return d.RootID
}

// GetRoot returns the root object of the mount.
func (d *Driver) GetRoot(ctx context.Context) (Obj, error) {
	if d.rootID() == micloud.RootID {
		return micloud.Root(), nil
	}
	return d.Client.Get(ctx, d.rootID())
}

// List lists the children of dir.
func (d *Driver) List(ctx context.Context, dir Obj) ([]Obj, error) {
	files, err := d.Client.List(ctx, asFile(dir))
	if err != nil {
		return nil, err
	}
	out := make([]Obj, len(files))
	for i, f := range files {
		out[i] = f
	}
	return out, nil
}

// Link returns a direct download URL for file.
func (d *Driver) Link(ctx context.Context, file Obj) (*Link, error) {
	l, err := d.Client.Link(ctx, file.GetID())
	if err != nil {
		return nil, err
	}
	link := &Link{URL: l.URL, Header: l.Header}
	if !l.Expires.IsZero() {
		exp := time.Until(l.Expires)
		link.Expiration = &exp
	}
	return link, nil
}

// MakeDir creates a subfolder of parentDir.
func (d *Driver) MakeDir(ctx context.Context, parentDir Obj, dirName string) (Obj, error) {
	return d.Client.MakeDir(ctx, parentDir.GetID(), dirName)
}

// Move relocates srcObj into dstDir.
func (d *Driver) Move(ctx context.Context, srcObj, dstDir Obj) (Obj, error) {
	if err := d.Client.Move(ctx, []*micloud.File{asFile(srcObj)}, dstDir.GetID()); err != nil {
		return nil, err
	}
	return srcObj, nil
}

// Rename renames srcObj.
func (d *Driver) Rename(ctx context.Context, srcObj Obj, newName string) (Obj, error) {
	if err := d.Client.Rename(ctx, srcObj.GetID(), newName); err != nil {
		return nil, err
	}
	return srcObj, nil
}

// Copy server-side copies srcObj into dstDir.
func (d *Driver) Copy(ctx context.Context, srcObj, dstDir Obj) (Obj, error) {
	if err := d.Client.Copy(ctx, []*micloud.File{asFile(srcObj)}, dstDir.GetID()); err != nil {
		return nil, err
	}
	return nil, nil
}

// Remove deletes obj.
func (d *Driver) Remove(ctx context.Context, obj Obj) error {
	return d.Client.Remove(ctx, []*micloud.File{asFile(obj)})
}

// Put uploads a file into dstDir. up receives the percentage complete, matching
// OpenList's UpdateProgress.
func (d *Driver) Put(ctx context.Context, dstDir Obj, name string, size int64, r io.Reader, up func(percentage float64)) (Obj, error) {
	opts := micloud.UploadOptions{}
	if up != nil && size > 0 {
		opts.Progress = func(uploaded int64) {
			up(float64(uploaded) / float64(size) * 100)
		}
	}
	return d.Client.Upload(ctx, dstDir.GetID(), name, r, opts)
}

// asFile recovers the concrete *micloud.File, or rebuilds a reference carrying
// the ID (and folder flag) so operations that only need the ID still work.
func asFile(o Obj) *micloud.File {
	if f, ok := o.(*micloud.File); ok {
		return f
	}
	if o.IsDir() {
		return micloud.FolderRef(o.GetID())
	}
	return &micloud.File{ID: o.GetID(), Name: o.GetName(), Type: micloud.TypeOther}
}
