package xiaomidrive

import (
	"encoding/json"
	"testing"
)

func TestFlexDecoding(t *testing.T) {
	var s struct {
		A flexString `json:"a"`
		B flexString `json:"b"`
		C flexString `json:"c"`
		N flexInt    `json:"n"`
		M flexInt    `json:"m"`
		K flexInt    `json:"k"`
	}
	// a: numeric id kept verbatim (no float rounding), b: string, c: null,
	// n: number, m: numeric string, k: null.
	in := `{"a":123456789012345678,"b":"abc","c":null,"n":1700000000000,"m":"42","k":null}`
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	if string(s.A) != "123456789012345678" {
		t.Errorf("numeric id lost precision: %q", s.A)
	}
	if string(s.B) != "abc" || s.C != "" {
		t.Errorf("string/null decode wrong: %q %q", s.B, s.C)
	}
	if int64(s.N) != 1700000000000 || int64(s.M) != 42 || s.K != 0 {
		t.Errorf("int decode wrong: %d %d %d", s.N, s.M, s.K)
	}
}

func TestRecordToFile(t *testing.T) {
	raw := `{"id":"42","parentId":0,"name":"a.txt","type":"doc","size":"2048","sha1":"abc",
	         "modifyTime":1700000000000,"localModifyTime":1699999999000}`
	var r rawRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	parent := &File{ID: "0", Type: TypeFolder, Path: "/docs"}
	f := r.toFile(parent)
	if f.ID != "42" || f.Name != "a.txt" || f.Type != TypeDoc || f.Size != 2048 {
		t.Errorf("unexpected file: %+v", f)
	}
	if f.ParentID != "0" {
		t.Errorf("parentId not backfilled from parent: %q", f.ParentID)
	}
	if f.Path != "/docs/a.txt" {
		t.Errorf("path = %q, want /docs/a.txt", f.Path)
	}
	// localModifyTime should win over modifyTime for ModTime.
	if got := f.ModTime().UnixMilli(); got != 1699999999000 {
		t.Errorf("ModTime = %d, want client-side 1699999999000", got)
	}
}
