package stages

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// podMTUScript prints "<ip> <mtu>" for the eth0 of each pod network
// namespace on a node. containerd names them under /var/run/netns, and a
// pod on host networking has none.
const podMTUScript = `for ns in $(ip netns list | cut -d' ' -f1); do
  mtu=$(ip -n "$ns" -o link show eth0 2>/dev/null | sed -n 's/.* mtu \([0-9]*\) .*/\1/p')
  addr=$(ip -n "$ns" -o -4 addr show eth0 2>/dev/null | awk '{print $4}')
  if [ -n "$mtu" ] && [ -n "$addr" ]; then echo "${addr%/*} $mtu"; fi
done
true
`

// Workload is what kluster restarts so its pods get a new interface: a
// Deployment, StatefulSet or DaemonSet by a rollout, a CloudNativePG Cluster
// instance by instance.
type Workload struct{ Namespace, Kind, Name string }

func (w Workload) String() string { return w.Namespace + "/" + w.Kind + "/" + w.Name }

// cnpgCluster is the owner kind of a CloudNativePG instance pod.
const cnpgCluster = "Cluster"

// rolled are the kinds `kubectl rollout restart` restarts.
var rolled = []string{"Deployment", "StatefulSet", "DaemonSet"}

// podSurvey measures the interface of every running pod on the pod network.
// Nodes holds a login to every Node of the cluster, by name.
type podSurvey struct {
	CP     stage.Exec
	Nodes  map[string]stage.Exec
	PodMTU int
}

// survey is one measurement: the pods whose interface is not at PodMTU, by
// the workload that restarts them, and the pods kluster leaves alone (a bare
// pod, a Job), each with its reason.
type survey struct {
	stale map[Workload][]kube.Pod
	alone []string
	pods  []kube.Pod
}

func (s survey) workloads() []Workload {
	ws := make([]Workload, 0, len(s.stale))
	for w := range s.stale {
		ws = append(ws, w)
	}
	// Databases last: every other workload is up again before PostgreSQL
	// switches over.
	slices.SortFunc(ws, func(a, b Workload) int {
		if (a.Kind == cnpgCluster) != (b.Kind == cnpgCluster) {
			if a.Kind == cnpgCluster {
				return 1
			}
			return -1
		}
		return strings.Compare(a.String(), b.String())
	})
	return ws
}

// mtus reads "<ip> <mtu>" lines from every node.
func (p podSurvey) mtus(ctx context.Context) (map[string]int, error) {
	got := map[string]int{}
	for name, ex := range p.Nodes {
		out, err := ex.Run(ctx, podMTUScript)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			ip, mtu, ok := strings.Cut(line, " ")
			if !ok {
				continue
			}
			n, err := strconv.Atoi(mtu)
			if err != nil {
				return nil, fmt.Errorf("%s: pod interface %q: %w", name, line, err)
			}
			got[ip] = n
		}
	}
	return got, nil
}

func (p podSurvey) take(ctx context.Context) (survey, error) {
	k := kube.Kubectl{Exec: p.CP}
	pods, err := k.Pods(ctx)
	if err != nil {
		return survey{}, err
	}
	rs, err := k.ReplicaSetOwners(ctx)
	if err != nil {
		return survey{}, err
	}
	mtus, err := p.mtus(ctx)
	if err != nil {
		return survey{}, err
	}
	s := survey{stale: map[Workload][]kube.Pod{}, pods: pods}
	for _, pod := range pods {
		if pod.Phase != "Running" || pod.HostNetwork || pod.IP == "" {
			continue
		}
		if _, ok := p.Nodes[pod.Node]; !ok {
			return survey{}, fmt.Errorf("pod %s/%s runs on %s, which kluster has no login to", pod.Namespace, pod.Name, pod.Node)
		}
		mtu, ok := mtus[pod.IP]
		if !ok {
			// Started after the read, or still starting: the next read sees it.
			s.alone = append(s.alone, fmt.Sprintf("%s/%s (not measured yet)", pod.Namespace, pod.Name))
			continue
		}
		if mtu == p.PodMTU {
			continue
		}
		w, why := workloadOf(pod, rs)
		if why != "" {
			s.alone = append(s.alone, fmt.Sprintf("%s/%s (MTU %d, %s)", pod.Namespace, pod.Name, mtu, why))
			continue
		}
		s.stale[w] = append(s.stale[w], pod)
	}
	return s, nil
}

// workloadOf is the workload that restarts pod, or why kluster leaves the
// pod alone.
func workloadOf(pod kube.Pod, rs map[string]kube.Owner) (Workload, string) {
	o := pod.Owner
	if o.Kind == "ReplicaSet" {
		o = rs[pod.Namespace+"/"+o.Name]
		if o.Kind == "" {
			return Workload{}, "a ReplicaSet without a Deployment"
		}
	}
	switch {
	case o.Kind == "":
		return Workload{}, "a bare pod"
	case o.Kind == cnpgCluster || slices.Contains(rolled, o.Kind):
		return Workload{Namespace: pod.Namespace, Kind: o.Kind, Name: o.Name}, ""
	default:
		return Workload{}, "owned by a " + o.Kind
	}
}

// PodMTURestart restarts every workload with a pod whose interface is not at
// PodMTU, one at a time, after FlannelMTU has run on every node: a pod gets
// its MTU only when it starts. Act waits for each restart and stops at the
// first one that does not finish within Wait, so a rollout that does not fit
// is not followed by others. A CloudNativePG Cluster restarts its replicas,
// switches over to one of them, then restarts the old primary. A bare pod
// or a Job is reported, never touched.
type PodMTURestart struct {
	Nodes  map[string]stage.Exec
	PodMTU int
	Wait   time.Duration
	Every  time.Duration
}

func (PodMTURestart) Name() string    { return "pod-mtu-restart" }
func (PodMTURestart) Runbook() string { return podMTURunbook }

func (r PodMTURestart) survey(ctx context.Context, h *stage.Host) (survey, error) {
	return podSurvey{CP: h.Exec, Nodes: r.Nodes, PodMTU: r.PodMTU}.take(ctx)
}

func aloneDetail(alone []string) string {
	if len(alone) == 0 {
		return ""
	}
	return "; left alone: " + strings.Join(alone, ", ")
}

func (r PodMTURestart) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	s, err := r.survey(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if len(s.stale) > 0 {
		var names []string
		for _, w := range s.workloads() {
			names = append(names, w.String())
		}
		return stage.Status{Detail: fmt.Sprintf("not yet: pods not at MTU %d in %s%s", r.PodMTU, strings.Join(names, ", "), aloneDetail(s.alone))}, nil
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("every workload's pods are at MTU %d%s", r.PodMTU, aloneDetail(s.alone))}, nil
}

func (r PodMTURestart) Preview(ctx context.Context, h *stage.Host) (string, error) {
	if h.Exec == nil {
		return "# restart, one at a time, each workload with a pod not at MTU " + strconv.Itoa(r.PodMTU) + "\n", nil
	}
	s, err := r.survey(ctx, h)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, w := range s.workloads() {
		if w.Kind == cnpgCluster {
			fmt.Fprintf(&b, "# %s: delete each replica pod, switch over to a replica, delete the old primary pod\n", w)
			continue
		}
		fmt.Fprintf(&b, "kubectl -n %s rollout restart %s/%s && kubectl -n %s rollout status %s/%s --timeout=%s\n",
			w.Namespace, strings.ToLower(w.Kind), w.Name, w.Namespace, strings.ToLower(w.Kind), w.Name, r.Wait)
	}
	for _, a := range s.alone {
		fmt.Fprintf(&b, "# left alone: %s\n", a)
	}
	return b.String(), nil
}

func (r PodMTURestart) Act(ctx context.Context, h *stage.Host) error {
	s, err := r.survey(ctx, h)
	if err != nil {
		return err
	}
	k := kube.Kubectl{Exec: h.Exec}
	for _, w := range s.workloads() {
		if w.Kind == cnpgCluster {
			err = r.restartDatabase(ctx, k, w, s.pods)
		} else {
			err = r.rollout(ctx, k, w)
		}
		if err != nil {
			return fmt.Errorf("%s: %w; the restarts after it did not run", w, err)
		}
	}
	return nil
}

func (r PodMTURestart) rollout(ctx context.Context, k kube.Kubectl, w Workload) error {
	ref := strings.ToLower(w.Kind) + "/" + w.Name
	if _, err := k.Run(ctx, "-n", w.Namespace, "rollout", "restart", ref); err != nil {
		return err
	}
	_, err := k.Run(ctx, "-n", w.Namespace, "rollout", "status", ref, "--timeout="+r.Wait.String())
	return err
}

// restartDatabase restarts every instance of a CloudNativePG Cluster: the
// replicas first, then a switchover, so the primary restarts as a replica. A
// Cluster of one instance has no replica: its one pod restarts, and
// PostgreSQL is down until it is back.
func (r PodMTURestart) restartDatabase(ctx context.Context, k kube.Kubectl, w Workload, pods []kube.Pod) error {
	db, err := k.Database(ctx, w.Namespace, w.Name)
	if err != nil {
		return err
	}
	var instances []string
	for _, p := range pods {
		if p.Namespace == w.Namespace && p.Owner == (kube.Owner{Kind: cnpgCluster, Name: w.Name}) && !p.Finished {
			instances = append(instances, p.Name)
		}
	}
	slices.Sort(instances)
	primary := db.CurrentPrimary
	for _, pod := range instances {
		if pod != primary {
			if err := r.restartInstance(ctx, k, w, pod); err != nil {
				return err
			}
		}
	}
	if target := firstOther(instances, primary); target != "" {
		if err := k.SetTargetPrimary(ctx, w.Namespace, w.Name, target); err != nil {
			return err
		}
		if err := r.until(ctx, "the switchover to "+target, func() (bool, error) {
			db, err := k.Database(ctx, w.Namespace, w.Name)
			return err == nil && db.CurrentPrimary == target && db.Phase == kube.CNPGHealthyPhase, err
		}); err != nil {
			return err
		}
	}
	return r.restartInstance(ctx, k, w, primary)
}

func firstOther(names []string, not string) string {
	for _, n := range names {
		if n != not {
			return n
		}
	}
	return ""
}

// restartInstance deletes one instance pod and waits until the operator has
// made it again and the Cluster is healthy. The delete returns once the old
// pod is gone, so a Ready pod of that name is the new one.
func (r PodMTURestart) restartInstance(ctx context.Context, k kube.Kubectl, w Workload, pod string) error {
	if _, err := k.Run(ctx, "-n", w.Namespace, "delete", "pod", pod, "--timeout="+r.Wait.String()); err != nil {
		return err
	}
	return r.until(ctx, pod+" back and the Cluster healthy", func() (bool, error) {
		ready, err := k.PodReady(ctx, w.Namespace, pod)
		if err != nil || !ready {
			return false, err
		}
		db, err := k.Database(ctx, w.Namespace, w.Name)
		return err == nil && db.Healthy() && db.Phase == kube.CNPGHealthyPhase, err
	})
}

// until waits up to Wait for done, and names what did not finish.
func (r PodMTURestart) until(ctx context.Context, what string, done func() (bool, error)) error {
	ok := false
	err := waitFor(ctx, r.Wait, r.Every, func() (stage.Status, error) {
		var err error
		ok, err = done()
		return stage.Status{Done: ok}, err
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s did not finish in %s", what, r.Wait)
	}
	return nil
}

// PodMTUProof is the end state, on the Control Plane: every Node is Ready,
// no pod is Pending, every CloudNativePG Cluster is healthy, and every
// workload's pods are at PodMTU. Act only waits up to Wait.
type PodMTUProof struct {
	Nodes  map[string]stage.Exec
	PodMTU int
	Wait   time.Duration
	Every  time.Duration
}

func (PodMTUProof) Name() string    { return "pod-mtu-proof" }
func (PodMTUProof) Runbook() string { return podMTURunbook }

func (p PodMTUProof) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	k := kube.Kubectl{Exec: h.Exec}
	s, err := podSurvey{CP: h.Exec, Nodes: p.Nodes, PodMTU: p.PodMTU}.take(ctx)
	if err != nil {
		return stage.Status{}, err
	}
	nodes, err := k.Nodes(ctx)
	if err != nil {
		return stage.Status{}, err
	}
	dbs, err := k.Databases(ctx)
	if err != nil {
		return stage.Status{}, err
	}
	var not []string
	for _, n := range nodes {
		if !n.Ready {
			not = append(not, n.Name+" Ready")
		}
	}
	for _, pod := range s.pods {
		if pod.Phase == "Pending" {
			not = append(not, fmt.Sprintf("%s/%s scheduled", pod.Namespace, pod.Name))
		}
	}
	for _, db := range dbs {
		if !db.Healthy() || db.Phase != kube.CNPGHealthyPhase {
			not = append(not, fmt.Sprintf("%s/%s healthy (%s)", db.Namespace, db.Name, db.Phase))
		}
	}
	for _, w := range s.workloads() {
		not = append(not, w.String()+" at MTU "+strconv.Itoa(p.PodMTU))
	}
	if len(not) > 0 {
		return stage.Status{Detail: "not yet: " + strings.Join(not, ", ")}, nil
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("%d nodes Ready, no pod Pending, %d databases healthy, every workload at MTU %d%s",
		len(nodes), len(dbs), p.PodMTU, aloneDetail(s.alone))}, nil
}

func (p PodMTUProof) Preview(context.Context, *stage.Host) (string, error) {
	return "# wait for every Node Ready, no pod Pending, every database healthy, every workload's pods at MTU " + strconv.Itoa(p.PodMTU) + "\n", nil
}

func (p PodMTUProof) Act(ctx context.Context, h *stage.Host) error {
	return waitFor(ctx, p.Wait, p.Every, func() (stage.Status, error) { return p.Probe(ctx, h) })
}
