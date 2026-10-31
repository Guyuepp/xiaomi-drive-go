package xiaomidrive

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestSearchKind(t *testing.T) {
	cases := map[string]string{
		"tag_0123456789abcdef0123456789abcdef": "tag",
		"tag":                                  "tag",
		"location_ab":                          "location",
		"time_ab":                              "time",
		"album+album_ab":                       "album",
		"album_ab":                             "album",
		"people":                               "people",
		"noResult":                             "noResult",
		"guide":                                "guide",
		"":                                     "",
	}
	for in, want := range cases {
		if got := searchKind(in); got != want {
			t.Errorf("searchKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestionsDecode(t *testing.T) {
	body := `{"expireMs":600000,"sections":[
	  {"title":"分类","type":"tag_aa","items":[{"title":"文档","url":"https://gallery.i.mi.com/result?title=%E6%96%87%E6%A1%A3&type=imageList&query=QQ","thumbnailUrl":"https://t/1"}]},
	  {"title":"分类","type":"tag","items":[{"title":"手写文档","count":0,"url":"https://gallery.i.mi.com/result?type=imageList&tag=x&tagId=217"},{"title":"no url"}]},
	  {"title":"相册+相册","type":"album+album_bb","items":[{"title":"2023","count":"12","url":"https://gallery.i.mi.com/result?query=AA"}]},
	  {"title":"无搜索结果","type":"noResult","items":[{"title":"x","url":"https://ignored"}]},
	  {"title":"搜索攻略","type":"guide","items":[{"title":"y","url":"https://ignored"}]}
	]}`
	var d rawSearchData
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	got := d.suggestions()
	if len(got) != 3 {
		t.Fatalf("got %d suggestions, want 3: %+v", len(got), got)
	}
	if got[0].Kind != "tag" || got[0].Title != "文档" || got[0].Section != "分类" || got[0].Thumbnail != "https://t/1" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Title != "手写文档" {
		t.Errorf("item without url not skipped: %+v", got[1])
	}
	if got[2].Kind != "album" || got[2].Count != 12 {
		t.Errorf("album = %+v", got[2])
	}

	var empty rawSearchData
	json.Unmarshal([]byte(`{"sections":[{"type":"noResult","items":[]},{"type":"guide","items":[]}]}`), &empty)
	if s := empty.suggestions(); len(s) != 0 {
		t.Errorf("no-result search gave %d suggestions", len(s))
	}
}

func TestPickImageList(t *testing.T) {
	high := "/gallery/user/image/list?type=imageList&accuracyMode=high&query=Q"
	low := "/gallery/user/image/list?type=imageList&accuracyMode=low&query=Q"
	noMode := "/gallery/user/image/list?type=imageList&query=Q"
	facet := "/gallery/user/list?facet=x"

	cases := []struct {
		name     string
		sections []rawResultSection
		want     string
	}{
		{"high after low", []rawResultSection{{Type: "imageList", DataURL: low}, {Type: "imageList", DataURL: high}}, high},
		{"facet ignored", []rawResultSection{{Type: "facet", DataURL: facet}, {Type: "imageList", DataURL: high}}, high},
		{"no mode is a fallback", []rawResultSection{{Type: "imageList", DataURL: noMode}}, noMode},
		{"low only gives nothing", []rawResultSection{{Type: "imageList", DataURL: low}}, ""},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		if got := pickImageList(c.sections); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestImageListQuery(t *testing.T) {
	sugg := url.Values{"tag": {"文档"}, "tagId": {"200007"}}
	q, err := imageListQuery("/gallery/user/image/list?type=imageList&accuracyMode=high&tag=%E6%96%87%E6%A1%A3&tagId=999999", sugg, "42")
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("tagId") != "200007" {
		t.Errorf("placeholder tagId not replaced: %q", q.Get("tagId"))
	}
	if q.Get("clientVersion") != "a" || q.Get("language") != "b" || q.Get("userId") != "42" {
		t.Errorf("required params missing: %v", q)
	}

	// Values already in the dataUrl are kept; a real tagId is not touched.
	q, err = imageListQuery("/gallery/user/image/list?tagId=5&userId=7&language=en_US", sugg, "42")
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("tagId") != "5" || q.Get("userId") != "7" || q.Get("language") != "en_US" || q.Get("accuracyMode") != "high" {
		t.Errorf("existing params changed: %v", q)
	}

	for _, bad := range []string{
		"https://evil.example/gallery/user/image/list?x=1",
		"//evil.example/gallery/user/image/list",
		"/gallery/user/list?facet=x",
		"/drive/v2/user/files/1",
	} {
		if _, err := imageListQuery(bad, sugg, "42"); err == nil {
			t.Errorf("dataUrl %q accepted", bad)
		}
	}
}

func TestGalleryBlockCooldown(t *testing.T) {
	g := NewClient().Gallery() // not logged in: a request that goes out fails with ErrUnauthorized
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }

	ctx := context.Background()
	if _, err := g.get(ctx, "/gallery/user/galleries", nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("before any block: %v, want the request to go out", err)
	}

	g.noteBlocked(&APIError{Endpoint: "/gallery/user/galleries", HTTPStatus: 200, Code: CodeGalleryBlocked, Reason: "user is blocked."})
	if _, err := g.get(ctx, "/gallery/user/search", nil); !errors.Is(err, ErrBlocked) {
		t.Fatalf("during cooldown: %v, want ErrBlocked without a request", err)
	}

	// Other errors do not extend or start a cooldown.
	g.noteBlocked(ErrNotFound)
	g.noteBlocked(&APIError{Endpoint: "/drive/v2/user/files", Code: CodeGalleryBlocked})
	now = now.Add(galleryBlockCooldown)
	if _, err := g.get(ctx, "/gallery/user/galleries", nil); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("after cooldown: %v, want the request to go out", err)
	}
}

func TestBlockedCodeMapping(t *testing.T) {
	gallery := &APIError{Endpoint: "/gallery/user/search", Code: 503, Reason: "user is blocked."}
	if !errors.Is(gallery, ErrBlocked) {
		t.Error("gallery 503 is not ErrBlocked")
	}
	drive := &APIError{Endpoint: "/drive/v2/user/files", Code: 503}
	if errors.Is(drive, ErrBlocked) {
		t.Error("drive 503 mapped to ErrBlocked")
	}
}
