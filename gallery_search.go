package xiaomidrive

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The search endpoints require clientVersion and language; any non-empty
// value is accepted. These are what the web client sends.
const (
	searchClientVersion = "a"
	searchLanguage      = "b"

	// searchPageSize is the most image/list returns per page.
	searchPageSize = 100

	// searchImageListPath is the only path a result section's dataUrl may
	// point at; anything else is rejected rather than requested.
	searchImageListPath = "/gallery/user/image/list"

	// searchTagIDPlaceholder is the tagId result puts into dataUrl for tag
	// suggestions; the real tagId from the suggestion must replace it.
	searchTagIDPlaceholder = "999999"
)

// Suggestion kinds (the prefix of the section type).
const (
	SearchKindTag      = "tag"
	SearchKindLocation = "location"
	SearchKindTime     = "time"
	SearchKindAlbum    = "album"
	SearchKindPeople   = "people"
)

// SearchSuggestion is one entry the service suggests for a query or on the
// navigation page: a category, place, time, album or person. Pass it to
// WalkSearch to get its photos.
//
// The service's search matches categories, places, times and album names. It
// does NOT match file names or text inside images.
type SearchSuggestion struct {
	Title string
	// Kind is SearchKindTag, SearchKindLocation, ... or another prefix the
	// service uses.
	Kind string
	// Section is the section title the service shows ("分类", "地点" ...).
	Section   string
	Count     int
	Thumbnail string
	// URL is the service's opaque link for this suggestion. It can be
	// stored and passed back later in a SearchSuggestion; do not build it.
	URL string
}

type rawSearchData struct {
	Sections []struct {
		Title string `json:"title"`
		Type  string `json:"type"`
		Items []struct {
			Title        string  `json:"title"`
			Count        flexInt `json:"count"`
			URL          string  `json:"url"`
			ThumbnailURL string  `json:"thumbnailUrl"`
		} `json:"items"`
	} `json:"sections"`
}

// searchKind turns a section type such as "tag_<hex>", "album+album_<hex>" or
// "noResult" into its kind prefix.
func searchKind(sectionType string) string {
	k, _, _ := strings.Cut(sectionType, "_")
	k, _, _ = strings.Cut(k, "+")
	return k
}

func (d *rawSearchData) suggestions() []*SearchSuggestion {
	var out []*SearchSuggestion
	for _, s := range d.Sections {
		kind := searchKind(s.Type)
		if kind == "noResult" || kind == "guide" {
			continue
		}
		for _, it := range s.Items {
			if it.URL == "" {
				continue
			}
			out = append(out, &SearchSuggestion{
				Title: it.Title, Kind: kind, Section: s.Title,
				Count: int(it.Count), Thumbnail: it.ThumbnailURL, URL: it.URL,
			})
		}
	}
	return out
}

func searchBase() url.Values {
	return url.Values{"clientVersion": {searchClientVersion}, "language": {searchLanguage}}
}

// Suggest asks the service what a query could mean and returns its
// suggestions, in the service's order. No suggestions means nothing matched.
// It sends one request; call it when the user submits a query, not per
// keystroke, since the search endpoints are quick to get the account blocked.
func (g *Gallery) Suggest(ctx context.Context, query string) ([]*SearchSuggestion, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: empty search query", ErrInvalidArgument)
	}
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	q := searchBase()
	q.Set("query", query)
	raw, err := g.get(ctx, "/gallery/user/search", q)
	if err != nil {
		return nil, err
	}
	var d rawSearchData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery search decode: %w", err)
	}
	return d.suggestions(), nil
}

// Navigation returns the service's browse entries (people, places,
// categories) shown before anything is typed.
func (g *Gallery) Navigation(ctx context.Context) ([]*SearchSuggestion, error) {
	if err := g.ensure(ctx); err != nil {
		return nil, err
	}
	raw, err := g.get(ctx, "/gallery/user/navigation", searchBase())
	if err != nil {
		return nil, err
	}
	var d rawSearchData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery navigation decode: %w", err)
	}
	return d.suggestions(), nil
}

type rawResultSection struct {
	Title   string `json:"title"`
	Type    string `json:"type"`
	DataURL string `json:"dataUrl"`
}

// pickImageList returns the dataUrl of the high-accuracy image list, falling
// back to the first image list. "low" lists are ignored: where measured they
// held the same photos.
func pickImageList(sections []rawResultSection) string {
	first := ""
	for _, s := range sections {
		if s.Type != "imageList" || s.DataURL == "" {
			continue
		}
		u, err := url.Parse(s.DataURL)
		if err != nil {
			continue
		}
		mode := u.Query().Get("accuracyMode")
		if mode == "high" {
			return s.DataURL
		}
		if first == "" && mode != "low" {
			first = s.DataURL
		}
	}
	return first
}

// imageListQuery validates a dataUrl and returns its query, with the tagId
// placeholder replaced by the suggestion's tagId and the required parameters
// filled in.
func imageListQuery(dataURL string, suggestion url.Values, userID string) (url.Values, error) {
	u, err := url.Parse(dataURL)
	if err != nil {
		return nil, fmt.Errorf("xiaomidrive: gallery search: bad dataUrl: %w", err)
	}
	if u.IsAbs() || u.Host != "" || u.Path != searchImageListPath {
		return nil, fmt.Errorf("xiaomidrive: gallery search: unexpected dataUrl path %q", u.Path)
	}
	q := u.Query()
	if q.Get("tagId") == searchTagIDPlaceholder {
		if id := suggestion.Get("tagId"); id != "" {
			q.Set("tagId", id)
		}
	}
	for k, v := range searchBase() {
		if q.Get(k) == "" {
			q[k] = v
		}
	}
	if q.Get("userId") == "" {
		q.Set("userId", userID)
	}
	if q.Get("accuracyMode") == "" {
		q.Set("accuracyMode", "high")
	}
	return q, nil
}

// WalkSearch calls fn for every photo behind a suggestion, 100 per request, in
// the service's order. That order is not by date (neither Taken, Modified nor
// Created). Return an error from fn to stop early (that error is returned).
// Photos in AlbumPrivate are never part of search results.
//
// Verified for category (with and without tagId), place, time, people and
// album suggestions.
func (g *Gallery) WalkSearch(ctx context.Context, s *SearchSuggestion, fn func(*Photo) error) error {
	if s == nil || s.URL == "" {
		return fmt.Errorf("%w: suggestion has no url", ErrInvalidArgument)
	}
	su, err := url.Parse(s.URL)
	if err != nil {
		return fmt.Errorf("xiaomidrive: gallery search: bad suggestion url: %w", err)
	}
	params := su.Query()
	if err := g.ensure(ctx); err != nil {
		return err
	}
	userID := g.c.s.userID()

	// result: suggestion -> data sections. The suggestion's own query
	// parameters are forwarded unchanged.
	rq := searchBase()
	rq.Set("userId", userID)
	for k, v := range params {
		rq[k] = v
	}
	raw, err := g.get(ctx, "/gallery/user/result", rq)
	if err != nil {
		return err
	}
	var rd struct {
		Sections []rawResultSection `json:"sections"`
	}
	if err := json.Unmarshal(raw, &rd); err != nil {
		return fmt.Errorf("xiaomidrive: gallery search result decode: %w", err)
	}
	dataURL := pickImageList(rd.Sections)
	if dataURL == "" {
		return nil
	}
	q, err := imageListQuery(dataURL, params, userID)
	if err != nil {
		return err
	}

	pos := 0
	for {
		q.Set("pos", strconv.Itoa(pos))
		q.Set("num", strconv.Itoa(searchPageSize))
		raw, err := g.get(ctx, searchImageListPath, q)
		if err != nil {
			return err
		}
		var d struct {
			NextPos    flexInt    `json:"nextPos"`
			IsLastPage flexBool   `json:"isLastPage"`
			Galleries  []rawPhoto `json:"galleries"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return fmt.Errorf("xiaomidrive: gallery search list decode: %w", err)
		}
		for i := range d.Galleries {
			if err := fn(d.Galleries[i].toPhoto()); err != nil {
				return err
			}
		}
		next := int(d.NextPos)
		if bool(d.IsLastPage) || len(d.Galleries) == 0 || next <= pos {
			return nil
		}
		pos = next
	}
}
