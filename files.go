package xiaomidrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Order is a server-side sort key for listing and searching.
type Order string

const (
	OrderName        Order = "NAME"
	OrderSize        Order = "SIZE"
	OrderServiceTime Order = "SERVICE_TIME"
)

// listPageLimit is the page size used when reading all pages. The service has
// no hard upper bound, but paging keeps responses and memory bounded.
const listPageLimit = 500

// ListOptions tunes a listing. The zero value lists everything by service time,
// newest first.
type ListOptions struct {
	Order   Order
	Reverse bool
	// OnlyFolders restricts the result to subfolders (the only effective
	// server-side type filter for listing).
	OnlyFolders bool
}

func (o ListOptions) order() Order {
	if o.Order == "" {
		return OrderServiceTime
	}
	return o.Order
}

type listData struct {
	Records  []rawRecord `json:"records"`
	HasMore  flexBool    `json:"hasMore"`
	AllCount int         `json:"allCount"`
}

// List returns all children of dir, reading every page. If dir is nil the root
// folder is listed.
func (c *Client) List(ctx context.Context, dir *File) ([]*File, error) {
	return c.ListWith(ctx, dir, ListOptions{})
}

// ListWith is List with explicit options.
func (c *Client) ListWith(ctx context.Context, dir *File, opts ListOptions) ([]*File, error) {
	if dir == nil {
		dir = Root()
	}
	var out []*File
	for page := 1; ; page++ {
		recs, hasMore, err := c.listPage(ctx, dir, page, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !hasMore || len(recs) == 0 {
			break
		}
	}
	return out, nil
}

func (c *Client) listPage(ctx context.Context, dir *File, pageNo int, opts ListOptions) ([]*File, bool, error) {
	typ := ""
	if opts.OnlyFolders {
		typ = string(TypeFolder)
	}
	q := url.Values{
		"parentId": {dir.ID},
		"pageNo":   {strconv.Itoa(pageNo)},
		"limit":    {strconv.Itoa(listPageLimit)},
		"type":     {typ},
		"order":    {string(opts.order())},
		"reverse":  {strconv.FormatBool(opts.Reverse)},
	}
	raw, err := c.s.get(ctx, "/drive/v2/user/folders/children", q)
	if err != nil {
		return nil, false, err
	}
	var d listData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, false, fmt.Errorf("xiaomidrive: list decode: %w", err)
	}
	out := make([]*File, 0, len(d.Records))
	for i := range d.Records {
		out = append(out, d.Records[i].toFile(dir))
	}
	return out, bool(d.HasMore), nil
}

// Get fetches a file or folder by ID. The root folder is returned directly.
func (c *Client) Get(ctx context.Context, id string) (*File, error) {
	if id == RootID || id == "" {
		return Root(), nil
	}
	raw, err := c.s.get(ctx, "/drive/v2/user/files/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var rec rawRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("xiaomidrive: get decode: %w", err)
	}
	if rec.ID == "" {
		rec.ID = flexString(id)
	}
	return rec.toFile(nil), nil
}

// DirPath resolves a folder ID to its slash path (without a leading slash),
// using the server's dir/get. It does not work for the root.
func (c *Client) DirPath(ctx context.Context, id string) (string, error) {
	if id == RootID {
		return "", nil
	}
	q := url.Values{"id": {id}}
	raw, err := c.s.get(ctx, "/drive/v2/user/records/dir/get", q)
	if err != nil {
		return "", err
	}
	var d struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", fmt.Errorf("xiaomidrive: dir path decode: %w", err)
	}
	return d.Path, nil
}

// GetByPath resolves a slash-separated path from the root to a File by walking
// each component. Matching is case-insensitive, as the service is. It returns
// ErrNotFound if any component is missing.
func (c *Client) GetByPath(ctx context.Context, p string) (*File, error) {
	p = strings.Trim(strings.ReplaceAll(p, "\\", "/"), "/")
	cur := Root()
	if p == "" {
		return cur, nil
	}
	for _, name := range strings.Split(p, "/") {
		if name == "" {
			continue
		}
		children, err := c.List(ctx, cur)
		if err != nil {
			return nil, err
		}
		var next *File
		for _, ch := range children {
			if strings.EqualFold(ch.Name, name) {
				next = ch
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("%w: %q under %q", ErrNotFound, name, cur.Path)
		}
		cur = next
	}
	return cur, nil
}

// MakeDir creates a folder under parentID. It is idempotent: an existing folder
// of the same name is returned unchanged.
func (c *Client) MakeDir(ctx context.Context, parentID, name string) (*File, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	form := url.Values{"name": {name}, "parentId": {parentID}}
	raw, err := c.s.postForm(ctx, "/drive/v2/user/folders/create", form)
	if err != nil {
		return nil, err
	}
	var rec rawRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("xiaomidrive: mkdir decode: %w", err)
	}
	f := rec.toFile(nil)
	if rec.Type == "" {
		f.Type = TypeFolder
	}
	return f, nil
}

// Rename changes an item's name. A name clash with a sibling returns ErrExist.
func (c *Client) Rename(ctx context.Context, id, newName string) error {
	if err := ValidateName(newName); err != nil {
		return err
	}
	form := url.Values{"id": {id}, "newName": {newName}, "channel": {"WEB"}}
	_, err := c.s.postForm(ctx, "/drive/v2/user/records/rename", form)
	return err
}

// SearchOptions tunes a search.
type SearchOptions struct {
	// Type restricts to a category (TypeDoc, TypePic, ...); empty means all.
	Type FileType
	// Label restricts to an exact extension, e.g. "txt".
	Label   string
	Order   Order
	Reverse bool
}

// Search finds files and folders whose name contains query (case-insensitive)
// anywhere in the drive, reading all pages.
func (c *Client) Search(ctx context.Context, query string, opts SearchOptions) ([]*File, error) {
	order := opts.Order
	if order == "" {
		order = OrderServiceTime
	}
	var out []*File
	for page := 1; ; page++ {
		q := url.Values{
			"pageNo":      {strconv.Itoa(page)},
			"limit":       {strconv.Itoa(listPageLimit)},
			"type":        {string(opts.Type)},
			"order":       {string(order)},
			"label":       {opts.Label},
			"queryString": {query},
			"reverse":     {strconv.FormatBool(opts.Reverse)},
		}
		raw, err := c.s.get(ctx, "/drive/v2/user/search", q)
		if err != nil {
			return nil, err
		}
		var d listData
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("xiaomidrive: search decode: %w", err)
		}
		for i := range d.Records {
			out = append(out, d.Records[i].toFile(nil))
		}
		if !bool(d.HasMore) || len(d.Records) == 0 {
			break
		}
	}
	return out, nil
}

// Quota reports storage usage.
func (c *Client) Quota(ctx context.Context) (*Quota, error) {
	raw, err := c.s.get(ctx, "/status/alldetail", nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		TotalQuota flexInt                `json:"totalQuota"`
		Used       flexInt                `json:"used"`
		UsedDetail map[string]quotaDetail `json:"usedDetail"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("xiaomidrive: quota decode: %w", err)
	}
	q := &Quota{Total: int64(d.TotalQuota), Used: int64(d.Used), UsedDetail: map[string]int64{}}
	for k, v := range d.UsedDetail {
		q.UsedDetail[k] = int64(v)
	}
	return q, nil
}

// quotaDetail decodes a usedDetail entry, which is an object
// {"size":<bytes>,"text":"<label>"}; a bare number is accepted too.
type quotaDetail int64

func (q *quotaDetail) UnmarshalJSON(b []byte) error {
	var obj struct {
		Size flexInt `json:"size"`
	}
	if len(b) > 0 && b[0] == '{' {
		if err := json.Unmarshal(b, &obj); err != nil {
			return err
		}
		*q = quotaDetail(obj.Size)
		return nil
	}
	var n flexInt
	if err := n.UnmarshalJSON(b); err != nil {
		return err
	}
	*q = quotaDetail(n)
	return nil
}
