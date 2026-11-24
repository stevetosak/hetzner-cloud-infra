// Package meshstore keeps the public key of every mesh machine in one central
// place (ADR 0016, Amendments). kluster reads a new key from its machine,
// writes it here and pushes each machine's config over SSH; no agent runs on a
// node. Argo CD does not manage the store.
package meshstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/vpn"
)

// The ConfigMap that holds the store: one data key per machine.
const (
	Namespace = "kube-system"
	Name      = "mesh-peers"
)

// Entry is what the store knows of one machine.
type Entry struct {
	Name      string     `json:"-"`
	Site      string     `json:"site"`
	Address   netip.Addr `json:"address"`
	PublicKey string     `json:"publicKey"`
}

// Store reads and writes the entries of the mesh.
type Store interface {
	// Entries reads every entry by machine name. A store that does not exist
	// yet has none.
	Entries(ctx context.Context) (map[string]Entry, error)
	// Put writes one entry and leaves the others as they are.
	Put(ctx context.Context, e Entry) error
	// Remove takes one machine's entry out and leaves the others; an entry
	// that is not there is no error.
	Remove(ctx context.Context, name string) error
}

// ConfigMapClient is the part of kube.Kubectl the store uses.
type ConfigMapClient interface {
	ConfigMap(ctx context.Context, namespace, name string) (map[string]string, bool, error)
	CreateConfigMap(ctx context.Context, namespace, name string) error
	PatchConfigMapData(ctx context.Context, namespace, name, key, value string) error
	RemoveConfigMapKey(ctx context.Context, namespace, name, key string) error
}

// ConfigMap is the Store in the ConfigMap kube-system/mesh-peers.
type ConfigMap struct {
	Client ConfigMapClient
}

// Entries reads the ConfigMap.
func (s ConfigMap) Entries(ctx context.Context) (map[string]Entry, error) {
	data, _, err := s.Client.ConfigMap(ctx, Namespace, Name)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]Entry, len(data))
	for name, v := range data {
		var e Entry
		if err := json.Unmarshal([]byte(v), &e); err != nil {
			return nil, fmt.Errorf("%s/%s: the entry of %s: %w", Namespace, Name, name, err)
		}
		e.Name = name
		entries[name] = e
	}
	return entries, nil
}

// Put refuses a malformed key, makes the ConfigMap on the first write and
// patches only this machine's data key.
func (s ConfigMap) Put(ctx context.Context, e Entry) error {
	if err := checkKey(e); err != nil {
		return err
	}
	_, found, err := s.Client.ConfigMap(ctx, Namespace, Name)
	if err != nil {
		return err
	}
	if !found {
		if err := s.Client.CreateConfigMap(ctx, Namespace, Name); err != nil {
			return err
		}
	}
	v, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.Client.PatchConfigMapData(ctx, Namespace, Name, e.Name, string(v))
}

// Remove removes only this machine's data key. A store that does not exist
// holds no entry, so there is nothing to remove.
func (s ConfigMap) Remove(ctx context.Context, name string) error {
	data, found, err := s.Client.ConfigMap(ctx, Namespace, Name)
	if err != nil {
		return err
	}
	if _, held := data[name]; !found || !held {
		return nil
	}
	return s.Client.RemoveConfigMapKey(ctx, Namespace, Name, name)
}

// Check refuses a store that does not agree with the Node set: anyone who can
// write the ConfigMap could otherwise add a peer, or move a machine's address.
// The Node set decides who is in the mesh; the store only adds the keys.
func Check(entries map[string]Entry, m *nodeset.Mesh) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		e := entries[name]
		errs = append(errs, checkKey(e))
		machine, declared := m.Machine(name)
		switch {
		case !declared:
			errs = append(errs, fmt.Errorf("%s/%s: %s is not a machine of the Node set's mesh; remove its data key from the ConfigMap", Namespace, Name, name))
		case e.Site != machine.Site.Name:
			errs = append(errs, fmt.Errorf("%s/%s: %s has site %q, but the Node set says %s", Namespace, Name, name, e.Site, machine.Site.Name))
		case e.Address != machine.Address:
			errs = append(errs, fmt.Errorf("%s/%s: %s has address %s, but the Node set says %s", Namespace, Name, name, e.Address, machine.Address))
		}
	}
	return errors.Join(errs...)
}

// checkKey accepts only the exact form `wg pubkey` prints.
func checkKey(e Entry) error {
	k, err := vpn.ParseKey(e.PublicKey)
	if err != nil || k.String() != e.PublicKey {
		return fmt.Errorf("%s/%s: %s has no valid WireGuard public key", Namespace, Name, e.Name)
	}
	return nil
}
