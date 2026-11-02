// Package export writes CSV and JSON for downloads. Every CSV cell is guarded
// against spreadsheet formula injection (security invariant 8).
package export

import (
	"encoding/csv"
	"encoding/json"
	"io"
)

// GuardCell prefixes a cell that a spreadsheet would read as a formula, that is
// one starting with = + - @ tab or carriage return, with a single quote.
func GuardCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// CSV writes guarded rows.
type CSV struct {
	w     *csv.Writer
	guard bool
}

// NewCSV returns a CSV writer that guards every cell.
func NewCSV(w io.Writer) *CSV { return &CSV{w: csv.NewWriter(w), guard: true} }

// newUnguarded is the negative control for the fuzz oracle: a writer that
// forgets the guard must be caught by it.
func newUnguarded(w io.Writer) *CSV { return &CSV{w: csv.NewWriter(w)} }

// Write writes one row.
func (c *CSV) Write(cells ...string) error {
	if c.guard {
		g := make([]string, len(cells))
		for i, s := range cells {
			g[i] = GuardCell(s)
		}
		cells = g
	}
	return c.w.Write(cells)
}

// Flush flushes buffered rows and returns the first write error.
func (c *CSV) Flush() error {
	c.w.Flush()
	return c.w.Error()
}

// JSONArray streams a JSON array element by element.
type JSONArray struct {
	w     io.Writer
	enc   *json.Encoder
	count int
	err   error
}

// NewJSONArray returns a writer for one array.
func NewJSONArray(w io.Writer) *JSONArray {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &JSONArray{w: w, enc: enc}
}

// Add appends one element.
func (a *JSONArray) Add(v any) error {
	if a.err != nil {
		return a.err
	}
	sep := "["
	if a.count > 0 {
		sep = ","
	}
	if _, a.err = io.WriteString(a.w, sep); a.err != nil {
		return a.err
	}
	a.err = a.enc.Encode(v)
	a.count++
	return a.err
}

// Close ends the array; an array without elements is [].
func (a *JSONArray) Close() error {
	if a.err != nil {
		return a.err
	}
	end := "]\n"
	if a.count == 0 {
		end = "[]\n"
	}
	_, a.err = io.WriteString(a.w, end)
	return a.err
}
