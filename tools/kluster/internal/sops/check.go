package sops

import (
	"errors"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// encToken is one value SOPS encrypted with AES-GCM.
var encToken = regexp.MustCompile(`^ENC\[AES256_GCM,data:[A-Za-z0-9+/=]*,iv:[A-Za-z0-9+/=]+,tag:[A-Za-z0-9+/=]+,type:[a-z]+\]$`)

// valueSections are what .sops.yaml encrypts in a Secret manifest.
var valueSections = []string{"data", "stringData"}

// recipientKinds are the sops metadata lists that name who can decrypt.
var recipientKinds = []string{"age", "pgp", "kms", "gcp_kms", "azure_kv", "hc_vault", "hckms"}

// CheckEncrypted reports why data, a Secret manifest, is not safe to commit.
// nil means: SOPS metadata with a MAC and at least one recipient, and every
// value under data and stringData an encrypted token. It is decided in Go,
// not by `sops filestatus`, which trusts the metadata alone: a file with the
// metadata and one value pasted in plaintext passes it.
//
// An error names keys, never values.
func CheckEncrypted(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// yaml's message quotes a line number, not the content.
		return fmt.Errorf("not YAML: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("not a YAML mapping")
	}
	root := doc.Content[0]

	var errs []error
	meta := lookup(root, "sops")
	if meta == nil || meta.Kind != yaml.MappingNode {
		return errors.New("no sops metadata: the file was never encrypted")
	}
	if mac := lookup(meta, "mac"); mac == nil || !encToken.MatchString(mac.Value) {
		errs = append(errs, errors.New("the sops metadata has no MAC"))
	}
	if !hasRecipient(meta) {
		errs = append(errs, errors.New("the sops metadata names no recipient"))
	}

	values := 0
	for _, section := range valueSections {
		n := lookup(root, section)
		if n == nil {
			continue
		}
		if n.Kind != yaml.MappingNode {
			errs = append(errs, fmt.Errorf("%s is not a mapping", section))
			continue
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, v := n.Content[i].Value, n.Content[i+1]
			values++
			if v.Kind != yaml.ScalarNode || !encToken.MatchString(v.Value) {
				errs = append(errs, fmt.Errorf("%s.%s is not encrypted", section, key))
			}
		}
	}
	if values == 0 {
		errs = append(errs, errors.New("no value under data or stringData"))
	}
	return errors.Join(errs...)
}

func hasRecipient(meta *yaml.Node) bool {
	for _, kind := range recipientKinds {
		if n := lookup(meta, kind); n != nil && n.Kind == yaml.SequenceNode && len(n.Content) > 0 {
			return true
		}
	}
	return false
}

// lookup returns the value of key in mapping m.
func lookup(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
