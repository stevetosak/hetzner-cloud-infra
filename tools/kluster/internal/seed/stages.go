// Package seed is `kluster services seed` (ADR 0010, Amendment 2026-10-22):
// it puts on a cluster what ArgoCD cannot or must not own — the CRDs, the
// Namespaces and the Secrets — installs ArgoCD, then waits until ArgoCD has
// built the rest and the zone points at the load balancer. Every Stage runs
// kubectl on the Control Plane: a rehearsal's laptop kubeconfig points at
// the live hub (package kube).
package seed

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/secrets"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// Runbook is the page whose sections the Stages carry out.
const Runbook = "docs/runbook/services-seed.md"

// Apply applies one manifest as one field manager. It is done when kubectl
// diff finds nothing to change.
type Apply struct {
	StageName string
	Manifest  []byte
	Target    Target
	// What names what the manifest holds, for output.
	What string

	last *Diff
}

func (a *Apply) Name() string    { return a.StageName }
func (a *Apply) Runbook() string { return Runbook + "#" + a.StageName }

func (a *Apply) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	d, err := diff(ctx, h, a.StageName, a.Manifest, a.Target)
	if err != nil {
		return stage.Status{}, err
	}
	a.last = &d
	if d.Empty() {
		return stage.Status{Done: true, Detail: a.What + " in step"}, nil
	}
	return stage.Status{Detail: fmt.Sprintf("%d object(s) of %s differ", len(d.Objects), a.What)}, nil
}

func (a *Apply) Preview(_ context.Context, h *stage.Host) (string, error) {
	s := fmt.Sprintf("kubectl apply %s  # %s\n", a.Target.flags(), a.What)
	if h.Exec != nil && a.last != nil {
		s += a.last.Summary()
	}
	return s, nil
}

func (a *Apply) Act(ctx context.Context, h *stage.Host) error {
	return apply(ctx, h, a.StageName, a.Manifest, a.Target)
}

// Secrets applies the Secret Catalog's Secrets of the seeded namespaces.
// Live applies each file's values with `kluster secrets` rules; a rehearsal
// makes each Secret fresh, once: a second seed must not rotate it.
type Secrets struct {
	Rehearsal bool
	Secrets   []*catalog.Secret
	// Store reads the files; live only.
	Store *secrets.Store

	diffs  []secrets.LiveDiff
	absent []*catalog.Secret
}

func (*Secrets) Name() string    { return "seed-secrets" }
func (*Secrets) Runbook() string { return Runbook + "#seed-secrets" }

func (s *Secrets) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	cl := kube.Kubectl{Exec: h.Exec}
	if !s.Rehearsal {
		diffs, err := secrets.Diff(ctx, s.Store, cl, s.Secrets)
		if err != nil {
			return stage.Status{}, err
		}
		s.diffs = diffs
		n := 0
		for _, d := range diffs {
			if d.Drift() {
				n++
			}
		}
		if n == 0 {
			return stage.Status{Done: true, Detail: fmt.Sprintf("%d Secret(s) in step with their files", len(diffs))}, nil
		}
		return stage.Status{Detail: fmt.Sprintf("%d Secret(s) differ from their files", n)}, nil
	}
	s.absent = nil
	present := map[catalog.Ref]bool{}
	for _, sec := range s.Secrets {
		live, err := cl.Secret(ctx, sec.Ref)
		if err != nil {
			return stage.Status{}, err
		}
		if live == nil {
			s.absent = append(s.absent, sec)
			continue
		}
		ok, err := secrets.HasKeys(live, sec)
		if err != nil {
			return stage.Status{}, fmt.Errorf("%s: %w", sec.Ref, err)
		}
		if !ok {
			return stage.Status{}, fmt.Errorf("%s exists without every key of %s: delete it, and the seed makes it fresh", sec.Ref, catalog.FileName)
		}
		present[sec.Ref] = true
	}
	// A copy made now must hold its source's value, so both are made together.
	for _, sec := range s.absent {
		for _, name := range sec.KeyNames() {
			if k := sec.Keys[name]; k.Kind == catalog.Copy && present[k.Source().Secret] {
				return stage.Status{}, fmt.Errorf("%s.%s copies %s, which exists: delete %s too, so both are made fresh together",
					sec.Ref, name, k.Source(), k.Source().Secret)
			}
		}
	}
	if len(s.absent) == 0 {
		return stage.Status{Done: true, Detail: fmt.Sprintf("%d fresh Secret(s) exist", len(s.Secrets))}, nil
	}
	return stage.Status{Detail: fmt.Sprintf("%d Secret(s) to make fresh", len(s.absent))}, nil
}

func (s *Secrets) Preview(_ context.Context, h *stage.Host) (string, error) {
	var b bytes.Buffer
	switch {
	case h.Exec == nil:
		fmt.Fprintf(&b, "apply %d Secret(s) as %s\n", len(s.Secrets), kube.FieldManager)
	case s.Rehearsal:
		for _, sec := range s.absent {
			fmt.Fprintf(&b, "make fresh: %s (%s)\n", sec.Ref, strings.Join(sec.KeyNames(), ", "))
		}
	default:
		if _, err := secrets.PrintDiff(&b, s.diffs); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func (s *Secrets) Act(ctx context.Context, h *stage.Host) error {
	cl := kube.Kubectl{Exec: h.Exec}
	if !s.Rehearsal {
		// No force: a key another manager owns is put right once, with
		// `kluster secrets apply --force-conflicts`.
		return secrets.Apply(ctx, s.Store, cl, s.diffs, false)
	}
	fresh, err := secrets.Fresh(s.Secrets)
	if err != nil {
		return err
	}
	for _, sec := range s.absent {
		if err := cl.Apply(ctx, fresh[sec.Ref], false); err != nil {
			return fmt.Errorf("%s: %w", sec.Ref, err)
		}
	}
	return nil
}
