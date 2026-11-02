package cube

import (
	"bytes"
	"unicode/utf8"
)

const maxExt = 16

// Ext returns the lower-cased text after the last '.' of name. It returns ""
// when there is no dot, the dot is first or last, the extension exceeds 16
// bytes, or it is not valid UTF-8.
func Ext(name []byte) string {
	i := bytes.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	e := name[i+1:]
	if len(e) > maxExt || !utf8.Valid(e) {
		return ""
	}
	return string(bytes.ToLower(e))
}
