package web

import (
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The files a page names in pageAssets must exist in the embedded static folder:
// a name that matches no file is a page without its styles, and the browser only
// finds out with a 404 under /static. The other way round, no ui-* file may be
// left that nothing loads.
func TestPageAssetsNameFilesThatExistAndNoUIFileIsLeftOver(t *testing.T) {
	have := map[string]bool{}
	entries, err := fs.ReadDir(files, "static")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		have[e.Name()] = true
	}
	used := map[string]bool{}
	for page, a := range pageAssets {
		for _, name := range append(append([]string{}, a.CSS...), a.JS...) {
			used[name] = true
			if !have[name] {
				t.Errorf("page %q names %q, which is not in static/", page, name)
			}
		}
	}
	// What the layout and the templates load by name, and what Go code names.
	ref := regexp.MustCompile(`\b(ui-[a-z0-9-]+\.(?:css|js))\b`)
	scan := func(raw []byte) {
		for _, m := range ref.FindAllSubmatch(raw, -1) {
			used[string(m[1])] = true
			if !have[string(m[1])] {
				t.Errorf("%q is named but not in static/", m[1])
			}
		}
	}
	err = fs.WalkDir(files, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(files, path)
		scan(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	goFiles, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range goFiles {
		if n := f.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			raw, err := os.ReadFile(n)
			if err != nil {
				t.Fatal(err)
			}
			scan(raw)
		}
	}
	for name := range have {
		if strings.HasPrefix(name, "ui-") && !used[name] {
			t.Errorf("static/%s is loaded by no page", name)
		}
	}
}
