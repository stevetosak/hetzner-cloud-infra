// Package stage runs the steps kluster takes on a host (ADR 0009).
//
// A Stage carries out one runbook section and names it. Its Probe is the
// read-only readback that section uses as proof; its Act changes the host.
// Plan Mode only probes and previews. Under --apply a Stage whose Probe says
// it is done is skipped, so a run that stopped halfway resumes where it
// stopped, and a Stage that acted must then pass its own Probe.
package stage

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Exec is the part of an SSH client a Stage uses.
type Exec interface {
	Run(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path, content string, mode os.FileMode) error
	ReadFile(ctx context.Context, path string) (string, error)
}

// Host is one machine a Stage runs on.
type Host struct {
	Name string
	Addr string // the address kluster reaches it on
	Exec Exec
}

// Status is a Probe's answer.
type Status struct {
	Done   bool
	Detail string
}

// Stage is one runbook section in code.
type Stage interface {
	Name() string
	// Runbook is the section this Stage carries out, as a repository path
	// with an anchor. A change to the Stage's commands changes that section
	// in the same pull request.
	Runbook() string
	// Probe reads the host and reports whether the section is done. It
	// never writes.
	Probe(ctx context.Context, h *Host) (Status, error)
	// Preview describes what Act would do: the commands for a new host, or
	// a diff of each file it would edit. It never writes. For a host that
	// does not exist yet, h.Exec is nil.
	Preview(ctx context.Context, h *Host) (string, error)
	Act(ctx context.Context, h *Host) error
}

// Options is how a run goes: Plan Mode unless Apply.
type Options struct {
	Apply bool
	Out   io.Writer
}

// Run runs stages in order on one host.
func Run(ctx context.Context, stages []Stage, h *Host, o Options) error {
	for _, s := range stages {
		st, err := s.Probe(ctx, h)
		if err != nil {
			return fmt.Errorf("%s: probe %s: %w", h.Name, s.Name(), err)
		}
		if st.Done {
			fmt.Fprintf(o.Out, "[%s] %s: done (%s) — %s\n", h.Name, s.Name(), st.Detail, s.Runbook())
			continue
		}
		preview, err := s.Preview(ctx, h)
		if err != nil {
			return fmt.Errorf("%s: preview %s: %w", h.Name, s.Name(), err)
		}
		if !o.Apply {
			fmt.Fprintf(o.Out, "[%s] %s: would run — %s\n%s", h.Name, s.Name(), s.Runbook(), indent(preview))
			continue
		}
		fmt.Fprintf(o.Out, "[%s] %s: running — %s\n%s", h.Name, s.Name(), s.Runbook(), indent(preview))
		if err := s.Act(ctx, h); err != nil {
			return fmt.Errorf("%s: %s: %w", h.Name, s.Name(), err)
		}
		st, err = s.Probe(ctx, h)
		if err != nil {
			return fmt.Errorf("%s: probe %s after acting: %w", h.Name, s.Name(), err)
		}
		if !st.Done {
			return fmt.Errorf("%s: %s acted but its probe still says not done (%s)", h.Name, s.Name(), st.Detail)
		}
		fmt.Fprintf(o.Out, "[%s] %s: done (%s)\n", h.Name, s.Name(), st.Detail)
	}
	return nil
}

// PlanNew prints what each stage would run on a host that does not exist
// yet, so Plan Mode shows a new server's commands before it is created.
func PlanNew(ctx context.Context, stages []Stage, name string, out io.Writer) error {
	h := &Host{Name: name}
	for _, s := range stages {
		preview, err := s.Preview(ctx, h)
		if err != nil {
			return fmt.Errorf("%s: preview %s: %w", name, s.Name(), err)
		}
		fmt.Fprintf(out, "[%s] %s: would run — %s\n%s", name, s.Name(), s.Runbook(), indent(preview))
	}
	return nil
}

// RunAll runs stages on every host in parallel and returns the first error
// once all hosts have finished. One host failing does not cancel the others:
// a shared cancel would kill their SSH sessions mid-command. Only the
// caller's ctx cancels. (Kept from feat/kluster-cli.)
func RunAll(ctx context.Context, stages []Stage, hosts []*Host, o Options) error {
	o.Out = &lockedWriter{w: o.Out}
	var g errgroup.Group
	for _, h := range hosts {
		g.Go(func() error { return Run(ctx, stages, h, o) })
	}
	return g.Wait()
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func indent(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return "    " + strings.Join(lines, "\n    ") + "\n"
}
