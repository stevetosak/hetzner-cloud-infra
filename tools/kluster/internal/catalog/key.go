package catalog

import (
	"errors"
	"fmt"
	"strings"
)

// Kind is where a key's value comes from (CONTEXT.md: Generated Secret,
// Issued Secret, Copied Secret).
type Kind string

const (
	// Generated values are random and made by kluster.
	Generated Kind = "generated"
	// Issued values come from a provider's console, through the password
	// manager.
	Issued Kind = "issued"
	// Copy values are another key's value, optionally inside a format.
	Copy Kind = "copy"
	// Literal values are written in the catalog: a non-secret value that
	// must still be inside a Secret.
	Literal Kind = "literal"
)

// Generated value lengths. Values are alphanumeric, about 5.95 bits a
// character: 40 is about 238 bits.
const (
	DefaultLength = 40
	MinLength     = 32
	MaxLength     = 256
)

// ValuePlaceholder is where a copy's format puts the copied value.
const ValuePlaceholder = "{value}"

// Key is one key of a Secret. The fields are flat; which of them a key may
// set depends on its Kind.
type Key struct {
	Kind Kind   `yaml:"kind"`
	Note string `yaml:"note"`

	// generated
	Length int `yaml:"length"`
	// issued: the Bitwarden item, and a field or an attachment of it.
	Item       string `yaml:"item"`
	Field      string `yaml:"field"`
	Attachment string `yaml:"attachment"`
	// RehearsalItem is the Bitwarden item a rehearsal reads instead of
	// Item, for a token made for rehearsals only; the same field or
	// attachment. Unset, a rehearsal has no value for the key (ADR 0009,
	// Amendment 2026-10-24).
	RehearsalItem string `yaml:"rehearsalItem"`
	// copy: the source key, and an optional format holding {value} once.
	From   string `yaml:"from"`
	Format string `yaml:"format"`
	// literal
	Value string `yaml:"value"`

	source KeyRef
}

// Source is the key a copy copies. It is the zero KeyRef for other kinds.
func (k *Key) Source() KeyRef { return k.source }

// InRehearsal reports whether a rehearsal has a value for the key: every
// kind but an issued key without a RehearsalItem.
func (k *Key) InRehearsal() bool { return k.Kind != Issued || k.RehearsalItem != "" }

// ForRehearsal returns the key as a rehearsal reads it: an issued key's
// RehearsalItem in place of its Item.
func (k *Key) ForRehearsal() *Key {
	r := *k
	if k.Kind == Issued {
		r.Item, r.RehearsalItem = k.RehearsalItem, ""
	}
	return &r
}

// Render returns the value of a copy, given its source's value.
func (k *Key) Render(source []byte) []byte {
	if k.Format == "" {
		return source
	}
	return []byte(strings.Replace(k.Format, ValuePlaceholder, string(source), 1))
}

// kindSpec is what a kind allows. A new kind is one entry in kinds.
type kindSpec struct {
	// fields the kind may set, beside kind and note.
	fields []string
	// copyable kinds may be the source of a copy.
	copyable bool
	check    func(k *Key) error
}

var kinds = map[Kind]kindSpec{
	Generated: {fields: []string{"length"}, copyable: true, check: func(k *Key) error {
		if k.Length == 0 {
			k.Length = DefaultLength
		}
		if k.Length < MinLength || k.Length > MaxLength {
			return fmt.Errorf("length %d is outside %d to %d", k.Length, MinLength, MaxLength)
		}
		return nil
	}},
	Issued: {fields: []string{"item", "field", "attachment", "rehearsalItem"}, copyable: true, check: func(k *Key) error {
		if k.Item == "" {
			return errors.New("an issued key names its Bitwarden item")
		}
		if (k.Field == "") == (k.Attachment == "") {
			return errors.New("an issued key names a field or an attachment, not both")
		}
		if k.RehearsalItem == k.Item {
			// A rehearsal never gets a live value (ADR 0009).
			return errors.New("rehearsalItem is the live item; a rehearsal needs a token of its own")
		}
		return nil
	}},
	Copy: {fields: []string{"from", "format"}, check: func(k *Key) error {
		src, err := ParseKeyRef(k.From)
		if err != nil {
			return fmt.Errorf("from: %w", err)
		}
		if k.Format != "" && strings.Count(k.Format, ValuePlaceholder) != 1 {
			return fmt.Errorf("format must hold %s exactly once", ValuePlaceholder)
		}
		k.source = src
		return nil
	}},
	Literal: {fields: []string{"value"}, check: func(k *Key) error {
		if k.Value == "" {
			return errors.New("a literal key has a value")
		}
		return nil
	}},
}

func (k Kind) spec() kindSpec { return kinds[k] }

func (k *Key) validate() error {
	spec, ok := kinds[k.Kind]
	if !ok {
		return fmt.Errorf("kind %q is not one of generated, issued, copy, literal", k.Kind)
	}
	allowed := map[string]bool{}
	for _, f := range spec.fields {
		allowed[f] = true
	}
	for name, set := range k.setFields() {
		if set && !allowed[name] {
			return fmt.Errorf("a %s key has no %s", k.Kind, name)
		}
	}
	return spec.check(k)
}

// setFields reports which kind-specific fields are set.
func (k *Key) setFields() map[string]bool {
	return map[string]bool{
		"length":        k.Length != 0,
		"item":          k.Item != "",
		"field":         k.Field != "",
		"attachment":    k.Attachment != "",
		"rehearsalItem": k.RehearsalItem != "",
		"from":          k.From != "",
		"format":        k.Format != "",
		"value":         k.Value != "",
	}
}
