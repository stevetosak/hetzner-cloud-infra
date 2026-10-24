package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// Waiting is how a waiting Stage polls: up to Timeout, every Poll.
type Waiting struct {
	Timeout time.Duration
	Poll    time.Duration
	// Sleep is time.Sleep unless a test replaces it.
	Sleep func(time.Duration)
}

// until probes until it says done or the time is up, then returns the last
// status.
func (w Waiting) until(ctx context.Context, probe func() (stage.Status, error)) (stage.Status, error) {
	sleep := w.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	for waited := time.Duration(0); ; waited += w.Poll {
		st, err := probe()
		if err != nil || st.Done || waited >= w.Timeout {
			return st, err
		}
		if err := ctx.Err(); err != nil {
			return st, err
		}
		sleep(w.Poll)
	}
}

// Healthy waits until every core Application is Synced and Healthy.
type Healthy struct {
	Applications []string
	Waiting
}

func (*Healthy) Name() string    { return "core-healthy" }
func (*Healthy) Runbook() string { return Runbook + "#core-healthy" }

func (c *Healthy) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	out, err := h.Exec.Run(ctx, adminEnv+"kubectl get applications.argoproj.io -n argocd -o json --ignore-not-found 2>/dev/null")
	if err != nil {
		// The CRD is not there before seed-crds: nothing is healthy yet.
		return stage.Status{Detail: "no Application can be read yet"}, nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Sync struct {
					Status string `json:"status"`
				} `json:"sync"`
				Health struct {
					Status string `json:"status"`
				} `json:"health"`
			} `json:"status"`
		} `json:"items"`
	}
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			return stage.Status{}, fmt.Errorf("reading the Applications: %w", err)
		}
	}
	state := map[string]string{}
	for _, it := range list.Items {
		state[it.Metadata.Name] = it.Status.Sync.Status + "/" + it.Status.Health.Status
	}
	var bad []string
	for _, app := range c.Applications {
		switch s, ok := state[app]; {
		case !ok:
			bad = append(bad, app+": missing")
		case s != "Synced/Healthy":
			bad = append(bad, app+": "+s)
		}
	}
	if len(bad) == 0 {
		return stage.Status{Done: true, Detail: fmt.Sprintf("%d Application(s) Synced and Healthy", len(c.Applications))}, nil
	}
	return stage.Status{Detail: strings.Join(bad, ", ")}, nil
}

func (c *Healthy) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("wait up to %s until Synced and Healthy: %s\n", c.Timeout, strings.Join(c.Applications, ", ")), nil
}

func (c *Healthy) Act(ctx context.Context, h *stage.Host) error {
	st, err := c.until(ctx, func() (stage.Status, error) { return c.Probe(ctx, h) })
	if err != nil {
		return err
	}
	if !st.Done {
		return fmt.Errorf("not healthy after %s: %s", c.Timeout, st.Detail)
	}
	return nil
}

// Address waits until the Gateway has its load balancer's public IPv4
// address, and hands it to the zone Stage.
type Address struct {
	Gateway, Namespace string
	Waiting
	// Got is the address once a Probe found it.
	Got *string
}

func (*Address) Name() string    { return "lb-address" }
func (*Address) Runbook() string { return Runbook + "#lb-address" }

func (a *Address) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	out, err := h.Exec.Run(ctx, adminEnv+"kubectl get gateway -n "+a.Namespace+" "+a.Gateway+
		" -o jsonpath='{.status.addresses[*].value}' --ignore-not-found 2>/dev/null")
	if err != nil {
		return stage.Status{Detail: "the Gateway cannot be read yet"}, nil
	}
	ip, err := publicIPv4(strings.Fields(out))
	if err != nil {
		return stage.Status{}, fmt.Errorf("Gateway %s/%s: %w", a.Namespace, a.Gateway, err)
	}
	if ip == "" {
		return stage.Status{Detail: "Gateway " + a.Namespace + "/" + a.Gateway + " has no public IPv4 address yet"}, nil
	}
	*a.Got = ip
	return stage.Status{Done: true, Detail: "load balancer at " + ip}, nil
}

// publicIPv4 picks the one public IPv4 address of a Gateway's addresses. A
// Hetzner load balancer also lists its IPv6 and its private address.
func publicIPv4(values []string) (string, error) {
	var found []string
	for _, v := range values {
		ip := net.ParseIP(v)
		if ip == nil || ip.To4() == nil || ip.IsPrivate() || ip.IsLoopback() {
			continue
		}
		found = append(found, v)
	}
	slices.Sort(found)
	found = slices.Compact(found)
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("several public IPv4 addresses %v; the zone check needs one", found)
}

func (a *Address) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("wait up to %s until Gateway %s/%s has a public IPv4 address\n", a.Timeout, a.Namespace, a.Gateway), nil
}

func (a *Address) Act(ctx context.Context, h *stage.Host) error {
	st, err := a.until(ctx, func() (stage.Status, error) { return a.Probe(ctx, h) })
	if err != nil {
		return err
	}
	if !st.Done {
		return fmt.Errorf("after %s: %s", a.Timeout, st.Detail)
	}
	return nil
}
