package micloud

import (
	"net/http"
	"path"
	"time"
)

// RootID is the ID of the drive's root folder.
const RootID = "0"

// FileType is the category the service assigns by extension.
type FileType string

const (
	TypeFolder FileType = "folder"
	TypeDoc    FileType = "doc"
	TypePic    FileType = "pic"
	TypeVideo  FileType = "video"
	TypeAudio  FileType = "audio"
	TypeOther  FileType = "other"
)

// File is a file or folder on the drive.
//
// The accessor methods follow OpenList's model.Obj (GetID, GetName, GetSize,
// ModTime, CreateTime, IsDir, GetPath) so a driver can wrap it directly.
type File struct {
	ID       string
	ParentID string
	Name     string
	Type     FileType
	Size     int64
	// SHA1 of the whole file, lower-case hex. Empty for folders and for
	// search results.
	SHA1 string
	// Label is the lower-case extension the service derived from the name.
	Label    string
	Revision string
	Created  time.Time
	Modified time.Time
	// LocalCreated / LocalModified are the client-side times sent at upload.
	LocalCreated  time.Time
	LocalModified time.Time
	// Path is the slash-separated path from the root ("/a/b.txt") when the
	// file was reached through a path lookup or a listing of a folder whose
	// path was known. It is empty otherwise.
	Path string
}

func (f *File) GetID() string   { return f.ID }
func (f *File) GetName() string { return f.Name }
func (f *File) GetSize() int64  { return f.Size }
func (f *File) GetPath() string { return f.Path }
func (f *File) IsDir() bool     { return f.Type == TypeFolder }

// ModTime prefers the client-side modification time recorded at upload.
func (f *File) ModTime() time.Time {
	if !f.LocalModified.IsZero() {
		return f.LocalModified
	}
	return f.Modified
}

// CreateTime prefers the client-side creation time recorded at upload.
func (f *File) CreateTime() time.Time {
	if !f.LocalCreated.IsZero() {
		return f.LocalCreated
	}
	return f.Created
}

// IsRoot reports whether f is the root folder.
func (f *File) IsRoot() bool { return f.ID == RootID }

func (f *File) childPath(name string) string {
	if f == nil || f.Path == "" {
		return ""
	}
	return path.Join(f.Path, name)
}

// Root returns a reference to the root folder.
func Root() *File {
	return &File{ID: RootID, Name: "", Type: TypeFolder, Path: "/"}
}

// FolderRef returns a reference to a folder known only by ID.
func FolderRef(id string) *File {
	if id == RootID {
		return Root()
	}
	return &File{ID: id, Type: TypeFolder}
}

// Link is a direct download URL. It needs no cookies and supports Range.
type Link struct {
	URL string
	// Header holds headers the request must carry (currently none).
	Header http.Header
	// Expires is when the URL stops working.
	Expires time.Time
}

// Quota is the account's storage usage in bytes.
type Quota struct {
	Total int64
	Used  int64
	// UsedDetail breaks usage down by service (Drive, GalleryImage, ...).
	UsedDetail map[string]int64
}

// Free returns the remaining space.
func (q *Quota) Free() int64 {
	if q.Total <= q.Used {
		return 0
	}
	return q.Total - q.Used
}

// TrashItem is an entry in the recycle bin. The service does not record the
// original folder or path.
type TrashItem struct {
	ID       string
	Name     string
	Type     FileType
	Label    string
	Size     int64
	Modified time.Time
}

// rawRecord is a file/folder record as the API returns it.
type rawRecord struct {
	ID              flexString `json:"id"`
	ParentID        flexString `json:"parentId"`
	Name            string     `json:"name"`
	Type            string     `json:"type"`
	Size            flexInt    `json:"size"`
	SHA1            string     `json:"sha1"`
	Label           string     `json:"label"`
	Revision        flexString `json:"revision"`
	Status          string     `json:"status"`
	CreateTime      flexInt    `json:"createTime"`
	ModifyTime      flexInt    `json:"modifyTime"`
	LocalCreateTime flexInt    `json:"localCreateTime"`
	LocalModifyTime flexInt    `json:"localModifyTime"`
}

func msTime(ms flexInt) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms))
}

func (r *rawRecord) toFile(parent *File) *File {
	f := &File{
		ID:            string(r.ID),
		ParentID:      string(r.ParentID),
		Name:          r.Name,
		Type:          FileType(r.Type),
		Size:          int64(r.Size),
		SHA1:          r.SHA1,
		Label:         r.Label,
		Revision:      string(r.Revision),
		Created:       msTime(r.CreateTime),
		Modified:      msTime(r.ModifyTime),
		LocalCreated:  msTime(r.LocalCreateTime),
		LocalModified: msTime(r.LocalModifyTime),
	}
	if f.Type == "" {
		f.Type = TypeOther
	}
	if parent != nil {
		if f.ParentID == "" {
			f.ParentID = parent.ID
		}
		f.Path = parent.childPath(f.Name)
	}
	return f
}
