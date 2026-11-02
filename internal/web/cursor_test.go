package web

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestCursor_RoundTrip(t *testing.T) {
	in := childCursor{V: 1, Scan: 3, Dir: 4, Sort: "size", Filter: "cat:video", Key: -7, Name: []byte{0xff, 'a'}}
	var out childCursor
	if err := decodeCursor(encodeCursor(in), &out); err != nil {
		t.Fatal(err)
	}
	if out.V != in.V || out.Scan != 3 || out.Dir != 4 || out.Sort != "size" || out.Filter != "cat:video" || out.Key != -7 || string(out.Name) != "\xffa" {
		t.Errorf("round trip = %+v", out)
	}
}

func TestCursor_Rejects(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for name, s := range map[string]string{
		"empty":         "",
		"not base64":    "!!!",
		"padded":        base64.URLEncoding.EncodeToString([]byte(`{"v":1}`)),
		"not json":      enc("hello"),
		"unknown field": enc(`{"v":1,"scan":1,"dir":1,"extra":1}`),
		"trailing data": enc(`{"v":1,"scan":1,"dir":1}}`),
		"two objects":   enc(`{"v":1}{"v":1}`),
		"wrong type":    enc(`{"v":"1"}`),
		"too long":      strings.Repeat("A", maxCursorLen+1),
	} {
		var c errCursor
		if err := decodeCursor(s, &c); err == nil {
			t.Errorf("%s: accepted %q as %+v", name, s, c)
		}
	}
	var c errCursor
	if err := decodeCursor(enc(`{"v":1,"scan":1,"dir":1,"rowid":9}`), &c); err != nil || c.RowID != 9 {
		t.Errorf("valid cursor: %+v, %v", c, err)
	}
}
