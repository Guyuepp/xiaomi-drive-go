package micloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// TrashList returns all recycle-bin entries, following the cursor across pages.
// The service does not record an item's original folder or path.
func (c *Client) TrashList(ctx context.Context) ([]TrashItem, error) {
	var out []TrashItem
	cursor := ""
	for {
		q := url.Values{"cursor": {cursor}}
		raw, err := c.s.get(ctx, "/drive/user/trash", q)
		if err != nil {
			return nil, err
		}
		var d struct {
			Cursor  string      `json:"cursor"`
			HasMore flexBool    `json:"hasMore"`
			List    []rawRecord `json:"list"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("micloud: trash decode: %w", err)
		}
		for i := range d.List {
			r := &d.List[i]
			typ := FileType(r.Type)
			if typ == "" {
				typ = TypeOther
			}
			out = append(out, TrashItem{
				ID: string(r.ID), Name: r.Name, Type: typ, Label: r.Label,
				Size: int64(r.Size), Modified: msTime(r.ModifyTime),
			})
		}
		if !bool(d.HasMore) || d.Cursor == "" || len(d.List) == 0 {
			break
		}
		cursor = d.Cursor
	}
	return out, nil
}

// Restore moves a recycle-bin item back. If its original folder is gone the
// service recreates it; if a same-name item exists the restored one is
// auto-renamed.
func (c *Client) Restore(ctx context.Context, id string) error {
	_, err := c.s.postForm(ctx, "/drive/user/trash/"+url.PathEscape(id)+"/restore", nil)
	return err
}

// Purge permanently deletes a recycle-bin item. There is no "empty bin" call;
// purge items one by one.
func (c *Client) Purge(ctx context.Context, id string) error {
	_, err := c.s.postForm(ctx, "/drive/user/trash/"+url.PathEscape(id)+"/purge", nil)
	return err
}
