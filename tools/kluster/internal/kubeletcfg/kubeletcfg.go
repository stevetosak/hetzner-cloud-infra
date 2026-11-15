// Package kubeletcfg compares and edits a KubeletConfiguration field by
// field: the ConfigMap kubeadm keeps, a node's /var/lib/kubelet/config.yaml
// and the running kubelet's configz. kluster sets only top-level fields and
// leaves every other line as it is.
package kubeletcfg

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Parse reads a KubeletConfiguration as YAML or JSON.
func Parse(doc string) (map[string]any, error) {
	m := map[string]any{}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		return nil, fmt.Errorf("reading the kubelet configuration: %w", err)
	}
	return m, nil
}

// Configz reads the answer of the node proxy's /configz: the running
// kubelet's configuration under "kubeletconfig".
func Configz(raw string) (map[string]any, error) {
	var z struct {
		KubeletConfig map[string]any `json:"kubeletconfig"`
	}
	if err := json.Unmarshal([]byte(raw), &z); err != nil {
		return nil, fmt.Errorf("reading configz: %w", err)
	}
	if z.KubeletConfig == nil {
		return nil, fmt.Errorf("configz holds no kubeletconfig")
	}
	// Through YAML, so its numbers compare as Parse's do.
	b, err := json.Marshal(z.KubeletConfig)
	if err != nil {
		return nil, err
	}
	return Parse(string(b))
}

// Differ returns, sorted, the names of the fields of want that cfg does not
// hold with the same value.
func Differ(cfg, want map[string]any) ([]string, error) {
	w, err := normal(want)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, v := range w {
		if !reflect.DeepEqual(cfg[name], v) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// normal passes want through YAML, so it holds the same types Parse gives.
func normal(want map[string]any) (map[string]any, error) {
	b, err := yaml.Marshal(want)
	if err != nil {
		return nil, err
	}
	return Parse(string(b))
}

// Merge returns doc with each field of want set: a field doc has is replaced
// where it stands, a new one goes in at its place in the alphabetical order
// kubeadm writes. Every other line stays as it is. The result is read back:
// it must hold want and every other field unchanged.
func Merge(doc string, want map[string]any) (string, error) {
	before, err := Parse(doc)
	if err != nil {
		return "", err
	}
	blocks := topLevelBlocks(doc)
	for _, name := range sortedNames(want) {
		text, err := block(name, want[name])
		if err != nil {
			return "", err
		}
		blocks = setBlock(blocks, name, text)
	}
	var b strings.Builder
	for _, bl := range blocks {
		b.WriteString(bl.text)
	}
	merged := b.String()

	after, err := Parse(merged)
	if err != nil {
		return "", fmt.Errorf("the merged configuration does not read back: %w", err)
	}
	if d, err := Differ(after, want); err != nil || len(d) > 0 {
		return "", fmt.Errorf("the merged configuration does not hold %v (%v)", d, err)
	}
	for name := range want {
		delete(before, name)
		delete(after, name)
	}
	if !reflect.DeepEqual(before, after) {
		return "", fmt.Errorf("the merge changed a field outside %v", sortedNames(want))
	}
	return merged, nil
}

// blockOf is one top-level field with its lines; name is "" for lines
// before the first field (a comment, "---").
type blockOf struct {
	name, text string
}

// topLevelBlocks cuts doc at each line that starts a top-level field. A line
// that starts with a space or "- " (kubeadm writes a list at column 0)
// belongs to the field above it.
func topLevelBlocks(doc string) []blockOf {
	var blocks []blockOf
	cur := blockOf{}
	for _, line := range strings.SplitAfter(doc, "\n") {
		if line == "" {
			continue
		}
		if name, ok := fieldName(line); ok {
			if cur.text != "" || cur.name != "" {
				blocks = append(blocks, cur)
			}
			cur = blockOf{name: name}
		}
		cur.text += line
	}
	if cur.text != "" {
		blocks = append(blocks, cur)
	}
	if n := len(blocks); n > 0 && !strings.HasSuffix(blocks[n-1].text, "\n") {
		blocks[n-1].text += "\n"
	}
	return blocks
}

func fieldName(line string) (string, bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '-' || line[0] == '#' {
		return "", false
	}
	name, _, ok := strings.Cut(line, ":")
	if !ok || strings.ContainsAny(name, " \"'") {
		return "", false
	}
	return name, true
}

// block writes one field as kubeadm does: two spaces per level.
func block(name string, v any) (string, error) {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]any{name: v}); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// setBlock replaces the named field, or puts it before the first field that
// sorts after it.
func setBlock(blocks []blockOf, name, text string) []blockOf {
	for i, bl := range blocks {
		if bl.name == name {
			blocks[i].text = text
			return blocks
		}
	}
	at := len(blocks)
	for i, bl := range blocks {
		if bl.name != "" && bl.name > name {
			at = i
			break
		}
	}
	blocks = append(blocks, blockOf{})
	copy(blocks[at+1:], blocks[at:])
	blocks[at] = blockOf{name: name, text: text}
	return blocks
}

func sortedNames(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// defaultHardMemory is the kubelet's own evictionHard memory.available when
// the configuration sets none.
const defaultHardMemory = "100Mi"

// Allocatable is the memory a node with this capacity offers its pods under
// cfg: capacity − kubeReserved − systemReserved − the hard eviction
// threshold. The kubelet reports the same number as the Node's allocatable.
func Allocatable(capacity string, cfg map[string]any) (resource.Quantity, error) {
	c, err := resource.ParseQuantity(capacity)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("capacity %q: %w", capacity, err)
	}
	a := c.DeepCopy()
	for _, field := range []string{"kubeReserved", "systemReserved"} {
		v, ok, err := memoryOf(cfg, field)
		if err != nil {
			return resource.Quantity{}, err
		}
		if !ok {
			continue
		}
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return resource.Quantity{}, fmt.Errorf("%s.memory %q: %w", field, v, err)
		}
		a.Sub(q)
	}
	hard, err := hardMemory(cfg, c)
	if err != nil {
		return resource.Quantity{}, err
	}
	a.Sub(hard)
	return a, nil
}

// hardMemory is evictionHard memory.available as a quantity: a percentage
// is of the capacity.
func hardMemory(cfg map[string]any, capacity resource.Quantity) (resource.Quantity, error) {
	v := defaultHardMemory
	if m, ok := cfg["evictionHard"].(map[string]any); ok {
		if s, ok := m["memory.available"].(string); ok {
			v = s
		}
	}
	if p, ok := strings.CutSuffix(v, "%"); ok {
		var f float64
		if _, err := fmt.Sscanf(p, "%g", &f); err != nil {
			return resource.Quantity{}, fmt.Errorf("evictionHard memory.available %q: %w", v, err)
		}
		return *resource.NewQuantity(int64(float64(capacity.Value())*f/100), resource.BinarySI), nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("evictionHard memory.available %q: %w", v, err)
	}
	return q, nil
}

func memoryOf(cfg map[string]any, field string) (string, bool, error) {
	raw, ok := cfg[field]
	if !ok || raw == nil {
		return "", false, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", false, fmt.Errorf("%s is not a map", field)
	}
	v, ok := m["memory"]
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, fmt.Errorf("%s.memory is not a quantity string", field)
	}
	return s, true, nil
}

// With returns cfg with the fields of want set: the configuration a node
// will run once it has them.
func With(cfg, want map[string]any) (map[string]any, error) {
	w, err := normal(want)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(cfg)+len(w))
	for k, v := range cfg {
		out[k] = v
	}
	for k, v := range w {
		out[k] = v
	}
	return out, nil
}
