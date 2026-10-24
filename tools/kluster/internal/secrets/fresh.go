package secrets

import (
	"fmt"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// Fresh makes the manifests a rehearsal applies in place of each Secret's
// file, in memory (ADR 0009, Amendment 2026-10-22): generated keys new,
// copies of those, literals as written. No file is read, so no live value
// can reach a rehearsal. It refuses an issued key, and a copy of a Secret
// outside secrets; `kluster services check` refuses both first.
func Fresh(secrets []*catalog.Secret) (map[catalog.Ref][]byte, error) {
	vals := map[catalog.Ref]Values{}
	for _, s := range secrets {
		v := Values{}
		for _, name := range s.KeyNames() {
			switch k := s.Keys[name]; k.Kind {
			case catalog.Generated:
				b, err := Random(k.Length)
				if err != nil {
					return nil, err
				}
				v[name] = b
			case catalog.Literal:
				v[name] = []byte(k.Value)
			case catalog.Issued:
				return nil, fmt.Errorf("%s.%s: an issued key has no rehearsal value; mark the Secret rehearsal: %s", s.Ref, name, catalog.RehearsalOmit)
			}
		}
		vals[s.Ref] = v
	}
	// Copies once every source is made.
	for _, s := range secrets {
		for _, name := range s.KeyNames() {
			k := s.Keys[name]
			if k.Kind != catalog.Copy {
				continue
			}
			src := k.Source()
			sv, ok := vals[src.Secret][src.Key]
			if !ok {
				return nil, fmt.Errorf("%s.%s: copies %s, which the rehearsal does not make", s.Ref, name, src)
			}
			vals[s.Ref][name] = k.Render(sv)
		}
	}
	out := map[catalog.Ref][]byte{}
	for _, s := range secrets {
		m, err := render(s, vals[s.Ref])
		if err != nil {
			return nil, err
		}
		out[s.Ref] = m
	}
	return out, nil
}

// HasKeys reports whether a live Secret, as YAML, holds every key of s. A
// rehearsal Secret is made once: a second seed must not rotate it.
func HasKeys(live []byte, s *catalog.Secret) (bool, error) {
	_, v, err := parse(live)
	if err != nil {
		return false, err
	}
	for _, name := range s.KeyNames() {
		if _, ok := v[name]; !ok {
			return false, nil
		}
	}
	return true, nil
}
