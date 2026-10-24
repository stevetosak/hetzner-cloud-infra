// Package catalog loads and validates the Secret Catalog, `secrets.yaml` at
// the repository root (ADR 0003, Amendment 2026-10-06; CONTEXT.md: Secret
// Catalog). The catalog lists every Secret the operator owns: its encrypted
// file, its type and labels, and for each key the kind of value it holds and
// where that value comes from.
//
// The catalog holds no secret value. A `literal` key holds a value, but only
// one that is not secret and must still be inside a Secret.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the catalog's name at the repository root.
const FileName = "secrets.yaml"

// EncSuffix ends every encrypted Secret file (.sops.yaml matches it).
const EncSuffix = ".enc.yaml"

// DefaultType is the Secret type when an entry names none.
const DefaultType = "Opaque"

// Catalog is the loaded secrets.yaml.
type Catalog struct {
	// Secrets in name order.
	Secrets []*Secret
	byRef   map[Ref]*Secret
	// root is the directory that holds secrets.yaml. Every File is
	// relative to it.
	root string
}

// Ref names a Secret: namespace and name.
type Ref struct {
	Namespace string
	Name      string
}

func (r Ref) String() string { return r.Namespace + "/" + r.Name }

// KeyRef names one key of one Secret.
type KeyRef struct {
	Secret Ref
	Key    string
}

func (k KeyRef) String() string { return k.Secret.String() + "." + k.Key }

// Secret is one catalog entry.
type Secret struct {
	Ref Ref `yaml:"-"`
	// File is the encrypted manifest, relative to the repository root. Its
	// base name is always <name>.enc.yaml.
	File   string            `yaml:"file"`
	Type   string            `yaml:"type"`
	Labels map[string]string `yaml:"labels"`
	Note   string            `yaml:"note"`
	Keys   map[string]*Key   `yaml:"keys"`
	// Rehearsal is RehearsalOmit when a rehearsal makes no copy of this
	// Secret, with the reason in Note. Every other Secret in a rehearsal
	// namespace is made fresh in memory (ADR 0009, Amendment 2026-10-22).
	Rehearsal string `yaml:"rehearsal"`
}

// RehearsalOmit is the one value Secret.Rehearsal takes.
const RehearsalOmit = "omit"

// OmittedInRehearsal reports whether a rehearsal leaves the Secret out.
func (s *Secret) OmittedInRehearsal() bool { return s.Rehearsal == RehearsalOmit }

// KeyNames returns the Secret's key names in order.
func (s *Secret) KeyNames() []string {
	names := make([]string, 0, len(s.Keys))
	for n := range s.Keys {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Load reads and validates the catalog at path.
func Load(p string) (*Catalog, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading the Secret Catalog: %w", err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("Secret Catalog %s: %w", p, err)
	}
	return c, nil
}

// Parse reads and validates a catalog whose files are relative to root.
func Parse(data []byte, root string) (*Catalog, error) {
	var doc struct {
		Secrets map[string]*Secret `yaml:"secrets"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}
	c := &Catalog{byRef: map[Ref]*Secret{}, root: root}
	var errs []error
	for id, s := range doc.Secrets {
		ref, err := ParseRef(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if s == nil {
			errs = append(errs, fmt.Errorf("%s: empty entry", id))
			continue
		}
		s.Ref = ref
		if s.Type == "" {
			s.Type = DefaultType
		}
		c.byRef[ref] = s
		c.Secrets = append(c.Secrets, s)
	}
	sort.Slice(c.Secrets, func(i, j int) bool { return c.Secrets[i].Ref.String() < c.Secrets[j].Ref.String() })
	if len(errs) == 0 {
		errs = append(errs, c.validate())
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// Root is the directory that holds the catalog.
func (c *Catalog) Root() string { return c.root }

// Path returns the absolute path of a Secret's encrypted file.
func (c *Catalog) Path(s *Secret) string { return filepath.Join(c.root, filepath.FromSlash(s.File)) }

// Secret returns the entry for ref.
func (c *Catalog) Secret(ref Ref) (*Secret, bool) {
	s, ok := c.byRef[ref]
	return s, ok
}

// ByFile returns the entry whose File is rel (relative to the root, with
// forward slashes).
func (c *Catalog) ByFile(rel string) (*Secret, bool) {
	for _, s := range c.Secrets {
		if s.File == rel {
			return s, true
		}
	}
	return nil, false
}

// CopiesOf returns every key that is a copy of src, in order.
func (c *Catalog) CopiesOf(src KeyRef) []KeyRef {
	var out []KeyRef
	for _, s := range c.Secrets {
		for _, name := range s.KeyNames() {
			k := s.Keys[name]
			if k.Kind == Copy && k.source == src {
				out = append(out, KeyRef{Secret: s.Ref, Key: name})
			}
		}
	}
	return out
}

// ParseRef reads "<namespace>/<name>".
func ParseRef(s string) (Ref, error) {
	ns, name, ok := strings.Cut(s, "/")
	if !ok || !dnsLabel.MatchString(ns) || !dnsSubdomain.MatchString(name) {
		return Ref{}, fmt.Errorf("%q: an entry is named <namespace>/<name>", s)
	}
	return Ref{Namespace: ns, Name: name}, nil
}

// ParseKeyRef reads "<namespace>/<name>.<key>". A Secret name may hold dots,
// so the key is what follows the last one.
func ParseKeyRef(s string) (KeyRef, error) {
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return KeyRef{}, fmt.Errorf("%q: a key is named <namespace>/<name>.<key>", s)
	}
	ref, err := ParseRef(s[:i])
	if err != nil || !keyName.MatchString(s[i+1:]) {
		return KeyRef{}, fmt.Errorf("%q: a key is named <namespace>/<name>.<key>", s)
	}
	return KeyRef{Secret: ref, Key: s[i+1:]}, nil
}

var (
	dnsLabel     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	dnsSubdomain = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	// keyName is what Kubernetes accepts as a Secret key.
	keyName = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)
)

func (c *Catalog) validate() error {
	var errs []error
	files := map[string]Ref{}
	for _, s := range c.Secrets {
		fail := func(format string, a ...any) {
			errs = append(errs, fmt.Errorf("%s: "+format, append([]any{s.Ref}, a...)...))
		}
		switch {
		case s.File == "":
			fail("file is required")
		case path.IsAbs(s.File) || path.Clean(s.File) != s.File || strings.HasPrefix(s.File, "../"):
			fail("file %q must be a clean path inside the repository, with forward slashes", s.File)
		case path.Base(s.File) != s.Ref.Name+EncSuffix:
			// One Secret per file, named after it (ADR 0003, 2026-09-22).
			fail("file %q must be named %s%s", s.File, s.Ref.Name, EncSuffix)
		}
		if other, dup := files[s.File]; dup {
			fail("file %q is also the file of %s", s.File, other)
		}
		files[s.File] = s.Ref
		if s.Rehearsal != "" && s.Rehearsal != RehearsalOmit {
			fail("rehearsal: %q; the one value is %s", s.Rehearsal, RehearsalOmit)
		}
		if len(s.Keys) == 0 {
			fail("keys: at least one key is required")
		}
		for _, name := range s.KeyNames() {
			k := s.Keys[name]
			if !keyName.MatchString(name) {
				fail("key %q is not a valid Secret key", name)
				continue
			}
			if k == nil {
				fail("key %s: empty", name)
				continue
			}
			if err := k.validate(); err != nil {
				fail("key %s: %v", name, err)
			}
		}
	}
	// Copies are checked once every entry is known.
	for _, s := range c.Secrets {
		for _, name := range s.KeyNames() {
			k := s.Keys[name]
			if k == nil || k.Kind != Copy || k.source == (KeyRef{}) {
				continue
			}
			src, ok := c.byRef[k.source.Secret]
			var sk *Key
			if ok {
				sk = src.Keys[k.source.Key]
			}
			switch {
			case sk == nil:
				errs = append(errs, fmt.Errorf("%s: key %s: copies %s, which the catalog does not hold", s.Ref, name, k.source))
			case !sk.Kind.spec().copyable:
				// A copy of a copy would hide where the value is made, and
				// a literal is not a secret worth a copy.
				errs = append(errs, fmt.Errorf("%s: key %s: copies %s, a %s key; copy a generated or issued key", s.Ref, name, k.source, sk.Kind))
			}
		}
	}
	return errors.Join(errs...)
}
