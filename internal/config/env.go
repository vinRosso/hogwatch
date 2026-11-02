package config

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type leaf struct {
	path   string // dotted yaml path
	secret bool
	value  reflect.Value
}

// leaves walks a struct and collects every leaf field with its yaml path. Env
// names derive from this, so they cannot drift from the struct.
func leaves(v reflect.Value, prefix string, out *[]leaf) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if key == "" || key == "-" {
			continue
		}
		path := prefix + key
		if f.Type.Kind() == reflect.Struct {
			leaves(v.Field(i), path+".", out)
			continue
		}
		*out = append(*out, leaf{path: path, secret: f.Tag.Get("hogwatch") == "secret", value: v.Field(i)})
	}
}

func envName(path string) string {
	return "HOGWATCH_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

// envNames lists every environment variable the config reads, in field order.
func envNames() []string {
	var ls []leaf
	leaves(reflect.ValueOf(&Config{}).Elem(), "", &ls)
	names := make([]string, len(ls))
	for i, l := range ls {
		names[i] = envName(l.path)
	}
	return names
}

var durationType = reflect.TypeOf(time.Duration(0))

// lookup returns the variable's value; an empty value counts as unset.
func lookup(src Source, name string) (string, bool) {
	v, ok := src.LookupEnv(name)
	return v, ok && v != ""
}

// overlayEnv applies environment values on top of cfg. Errors name the
// variable, never its value.
func overlayEnv(cfg *Config, src Source) error {
	var ls []leaf
	leaves(reflect.ValueOf(cfg).Elem(), "", &ls)
	for _, l := range ls {
		name := envName(l.path)
		raw, ok := lookup(src, name)
		if l.secret {
			fname := name + "_FILE"
			fpath, fok := lookup(src, fname)
			if ok && fok {
				return fmt.Errorf("%s and %s are both set; use only one", name, fname)
			}
			if fok {
				b, err := src.ReadFile(fpath)
				if err != nil {
					return fmt.Errorf("%s: cannot read secret file: %w", fname, err)
				}
				s := string(b)
				if strings.HasSuffix(s, "\r\n") {
					s = s[:len(s)-2]
				} else {
					s = strings.TrimSuffix(s, "\n")
				}
				raw, ok = s, true
			}
		}
		if !ok {
			continue
		}
		if err := setValue(l.value, raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// setValue parses raw into v. Its errors never include raw.
func setValue(v reflect.Value, raw string) error {
	switch {
	case v.Type() == durationType:
		d, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return errors.New("invalid duration")
		}
		v.SetInt(int64(d))
	case v.Kind() == reflect.String:
		v.SetString(raw)
	case v.Kind() == reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return errors.New("invalid boolean")
		}
		v.SetBool(b)
	case v.Kind() == reflect.Int:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return errors.New("invalid integer")
		}
		v.SetInt(int64(n))
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.String:
		items := []string{}
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				items = append(items, p)
			}
		}
		v.Set(reflect.ValueOf(items))
	default:
		return fmt.Errorf("unsupported config field type %s", v.Type())
	}
	return nil
}
