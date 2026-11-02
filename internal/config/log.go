package config

import (
	"io"
	"log/slog"
	"os"
	"reflect"
	"runtime"
	"strings"
	"time"
)

// FromOS returns a Source backed by the real process environment.
func FromOS() Source {
	return Source{LookupEnv: os.LookupEnv, ReadFile: os.ReadFile, GOMAXPROCS: runtime.GOMAXPROCS(0)}
}

// NewLogger builds the process logger from the log settings.
func NewLogger(c Log, w io.Writer) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(c.Level)); err != nil {
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if c.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

const redacted = "[redacted]"

// LogValue renders the config as nested groups with secrets redacted.
func (c Config) LogValue() slog.Value {
	return groupValue(reflect.ValueOf(c))
}

func groupValue(v reflect.Value) slog.Value {
	t := v.Type()
	var attrs []slog.Attr
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if key == "" || key == "-" {
			continue
		}
		fv := v.Field(i)
		switch {
		case f.Type.Kind() == reflect.Struct:
			attrs = append(attrs, slog.Attr{Key: key, Value: groupValue(fv)})
		case f.Tag.Get("hogwatch") == "secret":
			s := ""
			if fv.Len() > 0 {
				s = redacted
			}
			attrs = append(attrs, slog.String(key, s))
		case f.Type == durationType:
			attrs = append(attrs, slog.String(key, time.Duration(fv.Int()).String()))
		default:
			attrs = append(attrs, slog.Any(key, fv.Interface()))
		}
	}
	return slog.GroupValue(attrs...)
}
