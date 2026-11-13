package coreset

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing/fstest"
)

// overlay is the repository with some files written in memory: what the
// check would read after an edit, before anything is written to disk. A
// written file hides the base's; a directory lists the entries of both.
type overlay struct {
	base    fs.FS
	written fstest.MapFS
	order   []string // the written paths, in the order first written
}

func newOverlay(base fs.FS) *overlay {
	return &overlay{base: base, written: fstest.MapFS{}}
}

func (o *overlay) Open(name string) (fs.File, error) {
	if _, ok := o.written[name]; ok {
		return o.written.Open(name)
	}
	f, err := o.base.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		// A directory that only written files make.
		return o.written.Open(name)
	}
	return f, err
}

// ReadDir merges the base's entries with the written ones; a written entry
// wins. fs.Glob reads directories through it.
func (o *overlay) ReadDir(name string) ([]fs.DirEntry, error) {
	base, baseErr := fs.ReadDir(o.base, name)
	mine, mineErr := fs.ReadDir(o.written, name)
	if baseErr != nil && mineErr != nil {
		return nil, baseErr
	}
	byName := map[string]fs.DirEntry{}
	for _, e := range base {
		byName[e.Name()] = e
	}
	for _, e := range mine {
		byName[e.Name()] = e
	}
	out := make([]fs.DirEntry, 0, len(byName))
	for _, e := range byName {
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return out, nil
}

func (o *overlay) write(name string, data []byte) {
	if _, ok := o.written[name]; !ok {
		o.order = append(o.order, name)
	}
	o.written[name] = &fstest.MapFile{Data: data, Mode: 0o644}
}

// edits returns each written file that differs from the base, in the order
// first written. Old is nil for a file the base does not have.
func (o *overlay) edits() []FileEdit {
	var out []FileEdit
	for _, name := range o.order {
		new := o.written[name].Data
		old, err := fs.ReadFile(o.base, name)
		if err != nil {
			old = nil
		}
		if old != nil && string(old) == string(new) {
			continue
		}
		out = append(out, FileEdit{Path: name, Old: old, New: new})
	}
	return out
}
