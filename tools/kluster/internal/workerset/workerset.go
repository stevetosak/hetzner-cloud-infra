// Package workerset reads and edits a Worker set: the `workers` map in a
// tfvars file of the workers Module. Each entry is one Worker's identity —
// its name, private address and VPN address — written down once (ADR 0006,
// ADR 0009). kluster edits the file for `node add` and `node remove` and
// runs no Terraform until the operator commits the edit.
//
// An edit changes the tokens of that one entry and nothing else, so every
// comment in the file stays. hclwrite has no API for one element of an
// object, so the entry is found among the expression's tokens; the result
// is parsed again and must hold exactly the set the edit intended.
package workerset

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// attribute is the variable the workers Module declares the set in.
const attribute = "workers"

// Worker is one entry of the set.
type Worker struct {
	Name       string
	PrivateIP  string
	VpnIP      string
	ServerType string
	Labels     map[string]string
}

// Set is a Worker set as read from its file.
type Set struct {
	Path    string
	src     []byte
	workers map[string]Worker
}

// Load reads the Worker set at path.
func Load(path string) (*Set, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the Worker set: %w", err)
	}
	return Parse(path, src)
}

// Parse reads a Worker set from src; path names it in errors.
func Parse(path string, src []byte) (*Set, error) {
	f, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing %s: %w", path, diags)
	}
	attrs, diags := f.Body.JustAttributes()
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing %s: %w", path, diags)
	}
	a, ok := attrs[attribute]
	if !ok {
		return nil, fmt.Errorf("%s declares no %s", path, attribute)
	}
	v, diags := a.Expr.Value(nil)
	if diags.HasErrors() {
		return nil, fmt.Errorf("%s: %s: %w", path, attribute, diags)
	}
	workers, err := decode(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Set{Path: path, src: src, workers: workers}, nil
}

func decode(v cty.Value) (map[string]Worker, error) {
	if !v.Type().IsObjectType() && !v.Type().IsMapType() {
		return nil, fmt.Errorf("%s is not a map", attribute)
	}
	out := map[string]Worker{}
	for it := v.ElementIterator(); it.Next(); {
		k, e := it.Element()
		name := k.AsString()
		w := Worker{Name: name, Labels: map[string]string{}}
		var errs []error
		for attr, dst := range map[string]*string{"private_ip": &w.PrivateIP, "vpn_ip": &w.VpnIP, "server_type": &w.ServerType} {
			s, err := stringAttr(e, attr)
			errs = append(errs, err)
			*dst = s
		}
		if e.Type().IsObjectType() && e.Type().HasAttribute("labels") {
			for lt := e.GetAttr("labels").ElementIterator(); lt.Next(); {
				lk, lv := lt.Element()
				if lv.Type() != cty.String {
					errs = append(errs, fmt.Errorf("label %s is not a string", lk.AsString()))
					continue
				}
				w.Labels[lk.AsString()] = lv.AsString()
			}
		}
		if err := errors.Join(errs...); err != nil {
			return nil, fmt.Errorf("worker %s: %w", name, err)
		}
		out[name] = w
	}
	return out, nil
}

func stringAttr(obj cty.Value, name string) (string, error) {
	if !obj.Type().IsObjectType() || !obj.Type().HasAttribute(name) {
		return "", fmt.Errorf("no %s", name)
	}
	v := obj.GetAttr(name)
	if v.Type() != cty.String || v.IsNull() {
		return "", fmt.Errorf("%s is not a string", name)
	}
	return v.AsString(), nil
}

// Names returns the Workers' names, sorted.
func (s *Set) Names() []string { return slices.Sorted(maps.Keys(s.workers)) }

// Get returns the named Worker.
func (s *Set) Get(name string) (Worker, bool) {
	w, ok := s.workers[name]
	return w, ok
}

// Workers returns every Worker, sorted by name.
func (s *Set) Workers() []Worker {
	out := make([]Worker, 0, len(s.workers))
	for _, n := range s.Names() {
		out = append(out, s.workers[n])
	}
	return out
}

// Bytes is the file as it was read.
func (s *Set) Bytes() []byte { return s.src }

// With returns the file with w appended to the set.
func (s *Set) With(w Worker) ([]byte, error) {
	if _, ok := s.workers[w.Name]; ok {
		return nil, fmt.Errorf("%s already declares worker %s", s.Path, w.Name)
	}
	f, toks, err := s.tokens()
	if err != nil {
		return nil, err
	}
	labels := map[string]cty.Value{}
	for k, v := range w.Labels {
		labels[k] = cty.StringVal(v)
	}
	labelsVal := cty.EmptyObjectVal
	if len(labels) > 0 {
		labelsVal = cty.ObjectVal(labels)
	}
	entry := hclwrite.Tokens{{Type: hclsyntax.TokenIdent, Bytes: []byte(w.Name)}, {Type: hclsyntax.TokenEqual, Bytes: []byte("=")}}
	entry = append(entry, hclwrite.TokensForValue(cty.ObjectVal(map[string]cty.Value{
		"private_ip":  cty.StringVal(w.PrivateIP),
		"vpn_ip":      cty.StringVal(w.VpnIP),
		"server_type": cty.StringVal(w.ServerType),
		"labels":      labelsVal,
	}))...)
	entry = append(entry, newline())

	last := len(toks) - 1
	out := slices.Clone(toks[:last])
	if out[len(out)-1].Type != hclsyntax.TokenNewline {
		out = append(out, newline())
	}
	out = append(append(out, entry...), toks[last])
	want := maps.Clone(s.workers)
	want[w.Name] = w
	return s.finish(f, out, want)
}

// Without returns the file with the named Worker's entry removed, and any
// comment lines directly above it.
func (s *Set) Without(name string) ([]byte, error) {
	if _, ok := s.workers[name]; !ok {
		return nil, fmt.Errorf("%s declares no worker %s", s.Path, name)
	}
	f, toks, err := s.tokens()
	if err != nil {
		return nil, err
	}
	start, end, err := entrySpan(toks, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	out := append(slices.Clone(toks[:start]), toks[end:]...)
	want := maps.Clone(s.workers)
	delete(want, name)
	return s.finish(f, out, want)
}

// tokens returns the parsed file and the tokens of the set's expression,
// which must be an object constructor: `{` … `}`.
func (s *Set) tokens() (*hclwrite.File, hclwrite.Tokens, error) {
	f, diags := hclwrite.ParseConfig(s.src, s.Path, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, nil, fmt.Errorf("parsing %s: %w", s.Path, diags)
	}
	a := f.Body().GetAttribute(attribute)
	if a == nil {
		return nil, nil, fmt.Errorf("%s declares no %s", s.Path, attribute)
	}
	toks := a.Expr().BuildTokens(nil)
	if len(toks) < 2 || toks[0].Type != hclsyntax.TokenOBrace || toks[len(toks)-1].Type != hclsyntax.TokenCBrace {
		return nil, nil, fmt.Errorf("%s: %s is not written as { … }, so kluster will not edit it", s.Path, attribute)
	}
	return f, toks, nil
}

// finish writes toks back as the set's expression, formats the file, and
// proves the result parses to want.
func (s *Set) finish(f *hclwrite.File, toks hclwrite.Tokens, want map[string]Worker) ([]byte, error) {
	f.Body().SetAttributeRaw(attribute, toks)
	out := hclwrite.Format(f.Bytes())
	got, err := Parse(s.Path, out)
	if err != nil {
		return nil, fmt.Errorf("the edited Worker set does not parse: %w", err)
	}
	if !maps.EqualFunc(got.workers, want, equal) {
		return nil, fmt.Errorf("the edited Worker set does not hold the intended Workers")
	}
	return out, nil
}

func equal(a, b Worker) bool {
	return a.Name == b.Name && a.PrivateIP == b.PrivateIP && a.VpnIP == b.VpnIP &&
		a.ServerType == b.ServerType && maps.Equal(a.Labels, b.Labels)
}

// entrySpan finds the tokens of one entry at the top level of the object:
// from the comment lines above its key to the newline after its value.
func entrySpan(toks hclwrite.Tokens, name string) (int, int, error) {
	depth := 0
	for i := 0; i < len(toks); i++ {
		switch toks[i].Type {
		case hclsyntax.TokenOBrace, hclsyntax.TokenOBrack, hclsyntax.TokenOParen:
			depth++
			continue
		case hclsyntax.TokenCBrace, hclsyntax.TokenCBrack, hclsyntax.TokenCParen:
			depth--
			continue
		}
		if depth != 1 || !atLineStart(toks, i) {
			continue
		}
		keyEnd, ok := key(toks, i, name)
		if !ok {
			continue
		}
		start := i
		for start > 1 && toks[start-1].Type == hclsyntax.TokenComment {
			start--
		}
		d := 0
		for j := keyEnd; j < len(toks); j++ {
			switch toks[j].Type {
			case hclsyntax.TokenOBrace, hclsyntax.TokenOBrack, hclsyntax.TokenOParen:
				d++
			case hclsyntax.TokenCBrace, hclsyntax.TokenCBrack, hclsyntax.TokenCParen:
				d--
				if d < 0 {
					return start, j, nil // the entry ends at the object's own }
				}
			case hclsyntax.TokenNewline, hclsyntax.TokenComma:
				if d == 0 {
					return start, j + 1, nil
				}
			case hclsyntax.TokenComment:
				// A line comment holds its own newline.
				if d == 0 && bytes.HasSuffix(toks[j].Bytes, []byte("\n")) {
					return start, j + 1, nil
				}
			}
		}
		return 0, 0, fmt.Errorf("the entry for %s has no end", name)
	}
	return 0, 0, fmt.Errorf("no entry for %s among the tokens of %s", name, attribute)
}

func atLineStart(toks hclwrite.Tokens, i int) bool {
	if i == 0 {
		return false
	}
	switch toks[i-1].Type {
	case hclsyntax.TokenOBrace, hclsyntax.TokenNewline, hclsyntax.TokenComma:
		return true
	case hclsyntax.TokenComment:
		return bytes.HasSuffix(toks[i-1].Bytes, []byte("\n"))
	}
	return false
}

// key reports whether the tokens at i are the key name followed by = or :,
// bare or quoted, and returns the index after the = or :.
func key(toks hclwrite.Tokens, i int, name string) (int, bool) {
	j := i
	switch {
	case toks[j].Type == hclsyntax.TokenIdent && string(toks[j].Bytes) == name:
		j++
	case toks[j].Type == hclsyntax.TokenOQuote && j+2 < len(toks) &&
		toks[j+1].Type == hclsyntax.TokenQuotedLit && string(toks[j+1].Bytes) == name &&
		toks[j+2].Type == hclsyntax.TokenCQuote:
		j += 3
	default:
		return 0, false
	}
	if j < len(toks) && (toks[j].Type == hclsyntax.TokenEqual || toks[j].Type == hclsyntax.TokenColon) {
		return j + 1, true
	}
	return 0, false
}

func newline() *hclwrite.Token {
	return &hclwrite.Token{Type: hclsyntax.TokenNewline, Bytes: []byte("\n")}
}
