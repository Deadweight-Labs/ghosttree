package server

import (
	"net/http"
	"reflect"
	"strings"
)

// movedProjects rewrites the project query parameter when it names a named
// project by a former address of its owner (REQ-623): clients keep the id they
// were set up with, and the entries land where the project lives now.
func movedProjects(r *http.Request) {
	h, ok := r.Context().Value(accessKey{}).(*accessHolder)
	if !ok || h.st == nil {
		return
	}
	q := r.URL.Query()
	changed := false
	for i, v := range q["project"] {
		if cur, moved := h.st.MovedProject(v); moved {
			q["project"][i], changed = cur, true
		}
	}
	if changed {
		r.URL.RawQuery = q.Encode()
	}
}

// movedProjectFields does the same for every string field a decoded request
// body calls "project", at any depth.
func movedProjectFields(r *http.Request, v any) {
	h, ok := r.Context().Value(accessKey{}).(*accessHolder)
	if !ok || h.st == nil {
		return
	}
	walkProjectFields(reflect.ValueOf(v), func(s string) (string, bool) { return h.st.MovedProject(s) }, 0)
}

func walkProjectFields(v reflect.Value, resolve func(string) (string, bool), depth int) {
	if depth > 8 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkProjectFields(v.Elem(), resolve, depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkProjectFields(v.Index(i), resolve, depth+1)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f, fv := t.Field(i), v.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous && name == "" {
				walkProjectFields(fv, resolve, depth+1)
				continue
			}
			if name == "project" && fv.Kind() == reflect.String && fv.CanSet() {
				if cur, moved := resolve(fv.String()); moved {
					fv.SetString(cur)
				}
				continue
			}
			walkProjectFields(fv, resolve, depth+1)
		}
	}
}
