package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

func TestGuardCell(t *testing.T) {
	for in, want := range map[string]string{
		"=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@x": "'@x", "\tx": "'\tx", "\rx": "'\rx",
		"'=x": "'=x", "plain": "plain", "a=b": "a=b", "": "",
	} {
		if got := GuardCell(in); got != want {
			t.Errorf("GuardCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONArray(t *testing.T) {
	var b bytes.Buffer
	if err := func() error { a := NewJSONArray(&b); return a.Close() }(); err != nil || strings.TrimSpace(b.String()) != "[]" {
		t.Fatalf("empty = %q, %v", b.String(), err)
	}
	b.Reset()
	a := NewJSONArray(&b)
	for i := 0; i < 3; i++ {
		if err := a.Add(map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var got []map[string]int
	if err := json.Unmarshal(b.Bytes(), &got); err != nil || len(got) != 3 || got[2]["i"] != 2 {
		t.Fatalf("parsed %v, %v from %q", got, err, b.String())
	}
}

// oracle reports why cell round-trips unsafely, or "".
func oracle(in string, newW func(*bytes.Buffer) *CSV) string {
	var b bytes.Buffer
	w := newW(&b)
	if err := w.Write(in, "x"); err != nil {
		return "write: " + err.Error()
	}
	if err := w.Flush(); err != nil {
		return "flush: " + err.Error()
	}
	r := csv.NewReader(&b)
	r.FieldsPerRecord = -1
	rec, err := r.Read()
	if err != nil {
		// encoding/csv normalises \r\n inside a quoted field, so a parse of our
		// own output must still succeed.
		return "parse: " + err.Error()
	}
	cell := rec[0]
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "unguarded cell " + cell
	}
	return ""
}

func guarded(b *bytes.Buffer) *CSV   { return NewCSV(b) }
func unguarded(b *bytes.Buffer) *CSV { return newUnguarded(b) }

func TestFuzzOracle_CatchesUnguarded(t *testing.T) {
	if oracle("=1+1", unguarded) == "" {
		t.Fatal("the oracle accepts an unguarded formula cell")
	}
	if got := oracle("=1+1", guarded); got != "" {
		t.Errorf("guarded writer rejected: %s", got)
	}
}
