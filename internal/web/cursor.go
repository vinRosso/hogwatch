package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

const (
	cursorVersion = 1
	maxCursorLen  = 1024
)

// errCursor is returned for a cursor that does not decode; handlers answer 400.
var errBadCursor = errors.New("web: bad cursor")

// childCursor is the opaque position after a page of children. It names the
// node, sort and filter it belongs to, so a cursor taken from one request is
// refused on another.
type childCursor struct {
	V      int    `json:"v"`
	Scan   int64  `json:"scan"`
	Dir    int64  `json:"dir"`
	Sort   string `json:"sort"`
	Filter string `json:"filter"`
	Key    int64  `json:"key"`
	Name   []byte `json:"name"`
}

// errCursor is the position after a page of scan errors.
type errCursor struct {
	V     int   `json:"v"`
	Scan  int64 `json:"scan"`
	Dir   int64 `json:"dir"`
	RowID int64 `json:"rowid"`
}

// encodeCursor returns v as base64url JSON.
func encodeCursor(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic("web: cursor does not marshal: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses s into v, which must be a pointer to one of the cursor
// structs above. Unknown fields, trailing data and bad base64 are errors; the
// caller checks the version and the fields that tie the cursor to a request.
func decodeCursor(s string, v any) error {
	if s == "" || len(s) > maxCursorLen {
		return errBadCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return errBadCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errBadCursor
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errBadCursor
	}
	return nil
}

// seqCursor is the position after a page of listing rows.
type seqCursor struct {
	V   int   `json:"v"`
	Job int64 `json:"job"`
	Seq int64 `json:"seq"`
}
