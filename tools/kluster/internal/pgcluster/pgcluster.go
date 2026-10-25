// Package pgcluster reads the database Cluster's files in core/cnpg/cluster
// (ADR 0009, Amendment 2026-10-24): the Archive Generation they name, the two
// renders `kluster db create` applies, the check that the recovery overlay
// agrees with the base, and the bump after a live recovery. It reads and
// writes files only; package db runs it against a cluster.
package pgcluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kbuild"
)

// The Cluster and the files it is built from, relative to the repository top.
const (
	ClusterDir   = "core/cnpg/cluster"
	RecoverDir   = ClusterDir + "/recover"
	FreshDir     = ClusterDir + "/fresh"
	ClusterFile  = FreshDir + "/pg-cluster.yaml"
	RecoverPatch = RecoverDir + "/recover.json"

	ClusterName = "tosak-pg-cluster"
	Namespace   = "pg-cluster"
	// ArchiveStore is the ObjectStore the Cluster archives to; SourceStore
	// the read-only one every recovery reads from (CONTEXT.md).
	ArchiveStore = "tosak-pg-backups"
	SourceStore  = "tosak-pg-backups-source"
)

// Generation is an Archive Generation: the N of tosak-pg-cluster-gN.
type Generation int

var generationName = regexp.MustCompile(`^` + ClusterName + `-g([1-9][0-9]*)$`)

// ParseGeneration reads a serverName.
func ParseGeneration(serverName string) (Generation, error) {
	m := generationName.FindStringSubmatch(serverName)
	if m == nil {
		return 0, fmt.Errorf("serverName %q is not %s-g<N>", serverName, ClusterName)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, err
	}
	return Generation(n), nil
}

// ServerName is the generation's name, which is also its bucket prefix
// without the slash.
func (g Generation) ServerName() string { return fmt.Sprintf("%s-g%d", ClusterName, g) }

// Prefix is the generation's path in a bucket.
func (g Generation) Prefix() string { return g.ServerName() + "/" }

// Next is the generation a recovery from g archives to.
func (g Generation) Next() Generation { return g + 1 }

func (g Generation) String() string { return fmt.Sprintf("g%d", g) }

// Mode is what db create builds.
type Mode string

const (
	// Fresh is an empty initdb Cluster.
	Fresh Mode = "fresh"
	// Recover is a Cluster bootstrapped from the current generation.
	Recover Mode = "recover"
)

// CreatedBy is the annotation every render carries. A Cluster without it
// was not made by db create, so a resume never mistakes one for its own.
const CreatedBy = "kluster.tosak.net/created-by"

// cluster is what kluster reads of a Cluster manifest.
type cluster struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Instances int `yaml:"instances"`
		Plugins   []struct {
			Name       string            `yaml:"name"`
			Parameters map[string]string `yaml:"parameters"`
		} `yaml:"plugins"`
	} `yaml:"spec"`
}

// Current is the generation the repository names: the serverName in
// pg-cluster.yaml.
func Current(fsys fs.FS) (Generation, error) {
	b, err := fs.ReadFile(fsys, ClusterFile)
	if err != nil {
		return 0, err
	}
	var c cluster
	if err := yaml.Unmarshal(b, &c); err != nil {
		return 0, fmt.Errorf("%s: %w", ClusterFile, err)
	}
	if len(c.Spec.Plugins) != 1 {
		return 0, fmt.Errorf("%s: want one plugin, the barman-cloud WAL archiver", ClusterFile)
	}
	p := c.Spec.Plugins[0].Parameters
	if p["barmanObjectName"] != ArchiveStore {
		return 0, fmt.Errorf("%s: archives to %q, want %s", ClusterFile, p["barmanObjectName"], ArchiveStore)
	}
	return ParseGeneration(p["serverName"])
}

// Instances is the Cluster's instance count in pg-cluster.yaml.
func Instances(fsys fs.FS) (int, error) {
	b, err := fs.ReadFile(fsys, ClusterFile)
	if err != nil {
		return 0, err
	}
	var c cluster
	if err := yaml.Unmarshal(b, &c); err != nil {
		return 0, fmt.Errorf("%s: %w", ClusterFile, err)
	}
	return c.Spec.Instances, nil
}

type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// CheckOverlay proves recover.json reads the generation pg-cluster.yaml names
// through the source store and archives to the next one. Each error names
// the op that is wrong.
func CheckOverlay(fsys fs.FS) []error {
	g, err := Current(fsys)
	if err != nil {
		return []error{err}
	}
	b, err := fs.ReadFile(fsys, RecoverPatch)
	if err != nil {
		return []error{err}
	}
	var ops []patchOp
	if err := json.Unmarshal(b, &ops); err != nil {
		return []error{fmt.Errorf("%s: %w", RecoverPatch, err)}
	}
	cur, next := g.ServerName(), g.Next().ServerName()
	const serverName = "/spec/plugins/0/parameters/serverName"
	var errs []error
	fail := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{RecoverPatch}, a...)...))
	}
	var tested, replaced, source, external bool
	for _, op := range ops {
		switch {
		case op.Op == "test" && op.Path == serverName:
			tested = true
			if s := str(op.Value); s != cur {
				fail("tests serverName %q; %s names %s", s, ClusterFile, cur)
			}
		case op.Op == "replace" && op.Path == serverName:
			replaced = true
			if s := str(op.Value); s != next {
				fail("archives to %q; the next generation is %s", s, next)
			}
		case op.Op == "add" && op.Path == "/spec/bootstrap/recovery":
			source = true
			var r struct {
				Source string `json:"source"`
			}
			if err := json.Unmarshal(op.Value, &r); err != nil || r.Source != cur {
				fail("bootstrap.recovery.source is %q; want %s", r.Source, cur)
			}
		case op.Op == "add" && op.Path == "/spec/externalClusters":
			external = true
			var ec []struct {
				Name   string `json:"name"`
				Plugin struct {
					Parameters map[string]string `json:"parameters"`
				} `json:"plugin"`
			}
			if err := json.Unmarshal(op.Value, &ec); err != nil || len(ec) != 1 {
				fail("want one external cluster")
				continue
			}
			p := ec[0].Plugin.Parameters
			if ec[0].Name != cur || p["serverName"] != cur {
				fail("the external cluster is %q with serverName %q; want %s for both", ec[0].Name, p["serverName"], cur)
			}
			if p["barmanObjectName"] != SourceStore {
				// The archive store is writable: a recovery must read through
				// the store that cannot write the generation it reads.
				fail("reads through %q; a recovery reads through the source store %s", p["barmanObjectName"], SourceStore)
			}
		}
	}
	for _, need := range []struct {
		ok   bool
		what string
	}{{tested, "a test of serverName"}, {replaced, "a replace of serverName"}, {source, "bootstrap.recovery"}, {external, "externalClusters"}} {
		if !need.ok {
			fail("no %s", need.what)
		}
	}
	return errs
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// Render builds the Cluster for mode from the repository at root. A
// recovery with targetTime set stops replay there (point in time). The
// render carries CreatedBy.
func Render(root string, mode Mode, targetTime *time.Time) ([]byte, error) {
	dir := FreshDir
	if mode == Recover {
		dir = RecoverDir
	} else if targetTime != nil {
		return nil, errors.New("a target time is for a recovery")
	}
	m, err := kbuild.Build(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return nil, err
	}
	if m.Size() != 1 {
		return nil, fmt.Errorf("%s builds %d objects; want the Cluster alone", dir, m.Size())
	}
	b, err := m.AsYaml()
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc["kind"] != "Cluster" {
		return nil, fmt.Errorf("%s builds a %v; want the Cluster", dir, doc["kind"])
	}
	meta := doc["metadata"].(map[string]any)
	ann, _ := meta["annotations"].(map[string]any)
	if ann == nil {
		ann = map[string]any{}
	}
	ann[CreatedBy] = "kluster db create --" + string(mode)
	meta["annotations"] = ann
	if targetTime != nil {
		rec, ok := dig(doc, "spec", "bootstrap", "recovery")
		if !ok {
			return nil, fmt.Errorf("%s has no bootstrap.recovery", dir)
		}
		rec["recoveryTarget"] = map[string]any{"targetTime": targetTime.UTC().Format(time.RFC3339)}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func dig(m map[string]any, keys ...string) (map[string]any, bool) {
	for _, k := range keys {
		n, ok := m[k].(map[string]any)
		if !ok {
			return nil, false
		}
		m = n
	}
	return m, true
}

// Bump moves the repository to the next generation after a live recovery:
// serverName in pg-cluster.yaml, and the tests, the source and the replace in
// recover.json. It edits the text, so every comment stays. The result must
// pass CheckOverlay.
func Bump(root string) (Generation, error) {
	fsys := os.DirFS(root)
	g, err := Current(fsys)
	if err != nil {
		return 0, err
	}
	n, nn := g.Next(), g.Next().Next()
	cf := filepath.Join(root, filepath.FromSlash(ClusterFile))
	b, err := os.ReadFile(cf)
	if err != nil {
		return 0, err
	}
	old := "serverName: " + g.ServerName() + "\n"
	if bytes.Count(b, []byte(old)) != 1 {
		return 0, fmt.Errorf("%s: want one line %q", ClusterFile, strings.TrimSpace(old))
	}
	b = bytes.Replace(b, []byte(old), []byte("serverName: "+n.ServerName()+"\n"), 1)

	rf := filepath.Join(root, filepath.FromSlash(RecoverPatch))
	p, err := os.ReadFile(rf)
	if err != nil {
		return 0, err
	}
	// The archive name first, so it is not bumped twice.
	quoted := func(g Generation) []byte { return []byte(`"` + g.ServerName() + `"`) }
	p = bytes.ReplaceAll(p, quoted(n), quoted(nn))
	p = bytes.ReplaceAll(p, quoted(g), quoted(n))

	if err := os.WriteFile(cf, b, 0o644); err != nil {
		return 0, err
	}
	if err := os.WriteFile(rf, p, 0o644); err != nil {
		return 0, err
	}
	if errs := CheckOverlay(os.DirFS(root)); len(errs) > 0 {
		return 0, fmt.Errorf("the bump does not pass its own check: %w", errors.Join(errs...))
	}
	return n, nil
}
