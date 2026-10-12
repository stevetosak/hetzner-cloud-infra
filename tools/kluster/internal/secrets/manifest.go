package secrets

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"maps"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// Values are one Secret's plaintext values by key. They are never printed.
type Values map[string][]byte

// Keys returns the key names in order.
func (v Values) Keys() []string { return slices.Sorted(maps.Keys(v)) }

// manifest is a Kubernetes Secret as kluster writes it.
type manifest struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   metadata          `yaml:"metadata"`
	Type       string            `yaml:"type"`
	Data       map[string]string `yaml:"data,omitempty"`
	StringData map[string]string `yaml:"stringData,omitempty"`
}

type metadata struct {
	Name      string            `yaml:"name"`
	Namespace string            `yaml:"namespace"`
	Labels    map[string]string `yaml:"labels,omitempty"`
}

// render writes the Secret manifest of s holding v. Values go under data,
// base64-encoded, never under stringData: a key that leaves stringData is
// not removed from the live Secret by a server-side apply.
func render(s *catalog.Secret, v Values) ([]byte, error) {
	m := manifest{
		APIVersion: "v1",
		Kind:       "Secret",
		Metadata:   metadata{Name: s.Ref.Name, Namespace: s.Ref.Namespace, Labels: s.Labels},
		Type:       s.Type,
		Data:       map[string]string{},
	}
	for k, b := range v {
		m.Data[k] = base64.StdEncoding.EncodeToString(b)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// shape is what a decrypted file says besides its values.
type shape struct {
	ref    catalog.Ref
	typ    string
	labels map[string]string
}

// parse reads a decrypted Secret manifest. Errors name keys, never values.
func parse(plain []byte) (shape, Values, error) {
	var m manifest
	if err := yaml.Unmarshal(plain, &m); err != nil {
		return shape{}, nil, fmt.Errorf("the decrypted file is not a Secret manifest: %w", err)
	}
	if m.Kind != "Secret" {
		return shape{}, nil, fmt.Errorf("the decrypted file is a %q, not a Secret", m.Kind)
	}
	v := Values{}
	for k, s := range m.Data {
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return shape{}, nil, fmt.Errorf("data.%s is not base64", k)
		}
		v[k] = b
	}
	for k, s := range m.StringData {
		if _, dup := v[k]; dup {
			return shape{}, nil, fmt.Errorf("%s is under both data and stringData", k)
		}
		v[k] = []byte(s)
	}
	typ := m.Type
	if typ == "" {
		typ = catalog.DefaultType
	}
	return shape{ref: catalog.Ref{Namespace: m.Metadata.Namespace, Name: m.Metadata.Name}, typ: typ, labels: m.Metadata.Labels}, v, nil
}

// matches reports whether a file's shape is what the catalog says. A nil
// and an empty label map are equal under maps.Equal.
func (sh shape) matches(s *catalog.Secret) bool {
	return sh.ref == s.Ref && sh.typ == s.Type && maps.Equal(sh.labels, s.Labels)
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }
