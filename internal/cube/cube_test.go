package cube

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestAgeBucket(t *testing.T) {
	start := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	edges := []time.Time{
		start.AddDate(0, 0, -7), start.AddDate(0, -1, 0), start.AddDate(0, -3, 0),
		start.AddDate(-1, 0, 0), start.AddDate(-2, 0, 0), start.AddDate(-5, 0, 0),
		start.AddDate(-10, 0, 0),
	}
	sec := time.Second
	for i, e := range edges {
		// edge i separates bucket i (newer) from bucket i+1 (older).
		if got := AgeBucket(e.Add(sec), start); got != uint8(i) {
			t.Errorf("edge %d +1s: got %d want %d", i, got, i)
		}
		if got := AgeBucket(e, start); got != uint8(i+1) {
			t.Errorf("edge %d exact: got %d want %d", i, got, i+1)
		}
		if got := AgeBucket(e.Add(-sec), start); got != uint8(i+1) {
			t.Errorf("edge %d -1s: got %d want %d", i, got, i+1)
		}
	}
	if got := AgeBucket(start.Add(time.Hour), start); got != 0 {
		t.Errorf("future: got %d", got)
	}
	if got := AgeBucket(start, start); got != 0 {
		t.Errorf("now: got %d", got)
	}
	if got := AgeBucket(time.Unix(0, 0), start); got != 7 {
		t.Errorf("epoch: got %d", got)
	}
}

func TestSizeBucket(t *testing.T) {
	cases := []struct {
		in   int64
		want uint8
	}{
		{0, 0}, {1<<20 - 1, 0}, {1 << 20, 1}, {10 << 20, 2}, {100 << 20, 3},
		{1 << 30, 4}, {10 << 30, 5}, {math.MaxInt64, 5}, {-1, 0},
	}
	for _, c := range cases {
		if got := SizeBucket(c.in); got != c.want {
			t.Errorf("SizeBucket(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestExt(t *testing.T) {
	cases := map[string]string{
		"a.MP4": "mp4", "x.tar.gz": "gz", ".bashrc": "", "a.": "", "noext": "",
		"a." + strings.Repeat("x", 17): "",
		"a." + strings.Repeat("X", 16): strings.Repeat("x", 16),
		"a.\xff":                       "",
		"":                             "", ".": "",
		"a.ÄB": "äb",
	}
	for in, want := range cases {
		if got := Ext([]byte(in)); got != want {
			t.Errorf("Ext(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCategoryStrings(t *testing.T) {
	for c := Category(0); c < NumCategories; c++ {
		got, err := ParseCategory(c.String())
		if err != nil || got != c {
			t.Errorf("round trip %d: %v %v", c, got, err)
		}
	}
	if Video.String() != "video" || DiskImage.String() != "disk-image" || Other != 7 {
		t.Error("constants/strings")
	}
	if _, err := ParseCategory("Video"); err == nil {
		t.Error("accepted Video")
	}
	if _, err := ParseCategory(""); err == nil {
		t.Error("accepted empty")
	}
}

func TestCellIndex(t *testing.T) {
	if (Cell{Category: 1, Age: 2, Size: 3}).Index() != (1*8+2)*6+3 {
		t.Error("index")
	}
	if (Cell{Category: 7, Age: 7, Size: 5}).Index() != NumCells-1 {
		t.Error("max index")
	}
}

func TestBlob_EmptyAndKnown(t *testing.T) {
	var a Acc
	b, err := a.Encode()
	if err != nil || !bytes.Equal(b, []byte{0x01}) {
		t.Fatalf("empty: %x %v", b, err)
	}
	got, err := Decode(b)
	if err != nil || got != a {
		t.Fatalf("decode empty: %v", err)
	}
	// cell 0 -> (300 bytes, 2 files); cell 5 -> (0 bytes, 1 file); cell 383 -> (1, 1)
	a.Add(Cell{0, 0, 0}, 300, 2)
	a.Add(Cell{0, 0, 5}, 0, 1)
	a.Add(Cell{7, 7, 5}, 1, 1)
	// deltas: idx0 -> 0; idx5 -> 5-0-1=4; idx383 -> 383-5-1=377 = 0xF9 0x02
	want := []byte{0x01, 0x00, 0xAC, 0x02, 0x02, 0x04, 0x00, 0x01, 0xF9, 0x02, 0x01, 0x01}
	got2, err := a.Encode()
	if err != nil || !bytes.Equal(got2, want) {
		t.Fatalf("encode: %x want %x (%v)", got2, want, err)
	}
	dec, err := Decode(Blob(want))
	if err != nil || dec != a {
		t.Fatalf("decode known: %v", err)
	}
}

func TestBlob_DecodeRejects(t *testing.T) {
	maxv := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}       // MaxInt64
	over := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01} // 2^63
	huge := bytes.Repeat([]byte{0xff}, 11)                                     // uvarint overflow
	cat := func(parts ...[]byte) Blob { return Blob(bytes.Join(parts, nil)) }
	cases := map[string]Blob{
		"empty input":       {},
		"nil":               nil,
		"wrong version":     {0x02},
		"version 0":         {0x00},
		"index 384":         {0x01, 0x80, 0x03, 0x01, 0x01},
		"index huge":        cat([]byte{0x01}, huge, []byte{1, 1}),
		"zero cell":         {0x01, 0x00, 0x00, 0x00},
		"truncated delta":   {0x01, 0x80},
		"truncated bytes":   {0x01, 0x00, 0x80},
		"truncated files":   {0x01, 0x00, 0x01},
		"bytes > MaxInt64":  cat([]byte{0x01, 0x00}, over, []byte{0x01}),
		"files > MaxInt64":  cat([]byte{0x01, 0x00, 0x01}, over),
		"uvarint overflow":  cat([]byte{0x01, 0x00}, huge, []byte{0x01}),
		"trailing":          {0x01, 0x00, 0x01, 0x01, 0xAA},
		"trailing on empty": {0x01, 0x00},
		"non-minimal":       {0x01, 0x00, 0x81, 0x00, 0x01},
		"past end":          {0x01, 0x7f, 0x01, 0x01, 0x7f, 0x01, 0x01, 0x7f, 0x01, 0x01, 0x7f, 0x01, 0x01},
	}
	for name, b := range cases {
		if _, err := Decode(b); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	// MaxInt64 itself is valid.
	if _, err := Decode(cat([]byte{0x01, 0x00}, maxv, []byte{0x01})); err != nil {
		t.Errorf("MaxInt64 rejected: %v", err)
	}
	// files>0 with bytes==0 is valid.
	if _, err := Decode(Blob{0x01, 0x00, 0x00, 0x01}); err != nil {
		t.Errorf("empty files rejected: %v", err)
	}
}

func TestEncode_NegativeRejected(t *testing.T) {
	var a Acc
	a.Add(Cell{1, 1, 1}, -1, 1)
	if _, err := a.Encode(); err == nil {
		t.Error("negative bytes accepted")
	}
	a = Acc{}
	a.Add(Cell{1, 1, 1}, 5, -1)
	if _, err := a.Encode(); err == nil {
		t.Error("negative files accepted")
	}
	// Add then cancel returns to zero, which encodes as empty.
	a = Acc{}
	a.Add(Cell{2, 2, 2}, 10, 1)
	a.Add(Cell{2, 2, 2}, -10, -1)
	if b, err := a.Encode(); err != nil || !bytes.Equal(b, []byte{1}) {
		t.Errorf("cancel: %x %v", b, err)
	}
}

func TestMerge(t *testing.T) {
	var a, b Acc
	a.Add(Cell{0, 0, 0}, 1, 1)
	b.Add(Cell{0, 0, 0}, 2, 3)
	b.Add(Cell{1, 0, 0}, 4, 5)
	a.Merge(&b)
	if a[0].Bytes != 3 || a[0].Files != 4 || a[Cell{1, 0, 0}.Index()].Bytes != 4 {
		t.Errorf("merge: %+v", a[0])
	}
}

func fill() Acc {
	var a Acc
	a.Add(Cell{uint8(Video), 0, 5}, 100, 1)
	a.Add(Cell{uint8(Video), 3, 5}, 200, 2)
	a.Add(Cell{uint8(Video), 3, 4}, 50, 1)
	a.Add(Cell{uint8(Image), 3, 5}, 7, 1)
	a.Add(Cell{uint8(Other), 7, 0}, 0, 4)
	return a
}

func TestFilter_Apply(t *testing.T) {
	a := fill()
	blob, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, f, err := Filter{}.Apply(blob)
	if err != nil || b != 357 || f != 9 {
		t.Errorf("zero filter: %d %d %v", b, f, err)
	}
	flt := Filter{Categories: 1 << Video, Sizes: 1 << 5}
	b, f, err = flt.Apply(blob)
	if err != nil || b != 300 || f != 3 {
		t.Errorf("video >=10GB: %d %d %v", b, f, err)
	}
	flt = Filter{Ages: 1 << 7}
	b, f, err = flt.Apply(blob)
	if err != nil || b != 0 || f != 4 {
		t.Errorf("age: %d %d %v", b, f, err)
	}
	if _, _, err := (Filter{}).Apply(Blob{9}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("corrupt blob: %v", err)
	}
	// Sum overflow is reported, not wrapped.
	var o Acc
	o[0].Bytes, o[0].Files = math.MaxInt64, 1
	o[1].Bytes, o[1].Files = 1, 1
	ob, err := o.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (Filter{}).Apply(ob); !errors.Is(err, ErrCorrupt) {
		t.Errorf("overflow: %v", err)
	}
}

func accFromBytes(data []byte) Acc {
	var a Acc
	for len(data) >= 5 {
		idx := int(data[0]) | int(data[1]&1)<<8
		if idx < NumCells {
			v := int64(data[2])<<40 | int64(data[3])<<20 | int64(data[4])
			if data[2]&0x80 != 0 {
				v = math.MaxInt64 - v
			}
			a[idx].Bytes = v
			a[idx].Files = int64(data[4]) >> (data[3] & 7)
		}
		data = data[5:]
	}
	return a
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 1, 2, 3, 4, 1, 1, 0x80, 9, 9})
	f.Fuzz(func(t *testing.T, data []byte) {
		a := accFromBytes(data)
		blob, err := a.Encode()
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decode(blob)
		if err != nil || got != a {
			t.Fatalf("round trip: %v", err)
		}
	})
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1})
	f.Add([]byte{1, 0, 0xAC, 2, 2, 4, 0, 1, 0xF9, 2, 1, 1})
	f.Add([]byte{1, 0x80, 0x03, 1, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		a, err := Decode(Blob(data))
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("non-ErrCorrupt error: %v", err)
			}
			return
		}
		out, err := a.Encode()
		if err != nil || !bytes.Equal(out, data) {
			t.Fatalf("not canonical: in %x out %x err %v", data, out, err)
		}
	})
}
