// Package kube runs kubectl on the Control Plane with its admin kubeconfig.
// kluster does not use a kubeconfig on the laptop: in a rehearsal it points
// at 10.100.0.1, which the laptop's WireGuard routes to the LIVE hub.
package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// AdminConf is the kubeconfig kubeadm writes on the Control Plane.
const AdminConf = "/etc/kubernetes/admin.conf"

// Kubectl runs kubectl on the Control Plane, as root.
type Kubectl struct {
	Exec stage.Exec
}

// Run runs kubectl with args, each quoted.
func (k Kubectl) Run(ctx context.Context, args ...string) (string, error) {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = local.Quote(a)
	}
	return k.Exec.Run(ctx, "KUBECONFIG="+AdminConf+" kubectl "+strings.Join(q, " "))
}

// Node is a Kubernetes Node as kluster compares it.
type Node struct {
	Name         string
	Ready        bool
	InternalIP   string
	Kubelet      string
	ControlPlane bool
	ProviderID   string
	// Unschedulable is set by a cordon, and so by a drain.
	Unschedulable bool
	// Uninitialized is the cloud provider's taint, there until the cloud
	// controller has seen the Node.
	Uninitialized bool
	// CapacityMemory and AllocatableMemory are the Node's memory as the
	// kubelet reports it, quantities such as 3911564Ki.
	CapacityMemory    string
	AllocatableMemory string
	// Labels are the Node's labels, every key.
	Labels map[string]string
}

// UninitializedTaint is set on a Node of a kubelet run with
// --cloud-provider=external.
const UninitializedTaint = "node.cloudprovider.kubernetes.io/uninitialized"

// Nodes reads every Node.
func (k Kubectl) Nodes(ctx context.Context) ([]Node, error) {
	out, err := k.Run(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}
	return ParseNodes([]byte(out))
}

// ParseNodes reads `kubectl get nodes -o json`.
func ParseNodes(b []byte) ([]Node, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				ProviderID    string                 `json:"providerID"`
				Unschedulable bool                   `json:"unschedulable"`
				Taints        []struct{ Key string } `json:"taints"`
			} `json:"spec"`
			Status struct {
				Conditions []struct{ Type, Status string }  `json:"conditions"`
				Addresses  []struct{ Type, Address string } `json:"addresses"`
				NodeInfo   struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
				Capacity    map[string]string `json:"capacity"`
				Allocatable map[string]string `json:"allocatable"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading the Node list: %w", err)
	}
	nodes := make([]Node, 0, len(list.Items))
	for _, it := range list.Items {
		n := Node{Name: it.Metadata.Name, Kubelet: it.Status.NodeInfo.KubeletVersion, ProviderID: it.Spec.ProviderID,
			Unschedulable: it.Spec.Unschedulable, CapacityMemory: it.Status.Capacity["memory"],
			AllocatableMemory: it.Status.Allocatable["memory"], Labels: it.Metadata.Labels}
		for _, t := range it.Spec.Taints {
			n.Uninitialized = n.Uninitialized || t.Key == UninitializedTaint
		}
		_, n.ControlPlane = it.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" {
				n.Ready = c.Status == "True"
			}
		}
		for _, a := range it.Status.Addresses {
			if a.Type == "InternalIP" && n.InternalIP == "" {
				n.InternalIP = a.Address
			}
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// LabelArgs is the kubectl command that sets the labels of set on a Node,
// changing a value it already has, and removes the keys of remove. Keys
// come in sorted order, so the same change is always the same command.
func LabelArgs(node string, set map[string]string, remove []string) []string {
	args := []string{"label", "node", node, "--overwrite"}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		args = append(args, k+"="+set[k])
	}
	for _, k := range slices.Sorted(slices.Values(remove)) {
		args = append(args, k+"-")
	}
	return args
}

// Label sets and removes labels on a Node (LabelArgs).
func (k Kubectl) Label(ctx context.Context, node string, set map[string]string, remove []string) error {
	_, err := k.Run(ctx, LabelArgs(node, set, remove)...)
	return err
}

// Pod is a Pod as a drain sees it.
type Pod struct {
	Namespace string
	Name      string
	// DaemonSet pods stay on a drained Node: `kubectl drain
	// --ignore-daemonsets` leaves them.
	DaemonSet bool
	// Mirror pods are the kubelet's static pods; a drain leaves them too.
	Mirror bool
	// Finished pods (Succeeded or Failed) run nothing.
	Finished bool
	// Phase and Reason are the pod's status; a pod the kubelet refused at
	// admission is Failed with a reason such as OutOfmemory.
	Phase, Reason string
	// MemoryRequest is what the scheduler counts for the pod: the larger of
	// its containers with its sidecars, and its largest init container,
	// plus its overhead.
	MemoryRequest resource.Quantity
}

// Remains reports whether the pod still holds work a drain must move.
func (p Pod) Remains() bool { return !p.DaemonSet && !p.Mirror && !p.Finished }

// PodsOn reads every pod scheduled to the named Node.
func (k Kubectl) PodsOn(ctx context.Context, node string) ([]Pod, error) {
	out, err := k.Run(ctx, "get", "pods", "--all-namespaces", "--field-selector", "spec.nodeName="+node, "-o", "json")
	if err != nil {
		return nil, err
	}
	return ParsePods([]byte(out))
}

// mirrorAnnotation marks the API's copy of a static pod.
const mirrorAnnotation = "kubernetes.io/config.mirror"

// ParsePods reads `kubectl get pods -o json`.
func ParsePods(b []byte) ([]Pod, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace       string                  `json:"namespace"`
				Name            string                  `json:"name"`
				Annotations     map[string]string       `json:"annotations"`
				OwnerReferences []struct{ Kind string } `json:"ownerReferences"`
			} `json:"metadata"`
			Spec   podSpec `json:"spec"`
			Status struct {
				Phase  string `json:"phase"`
				Reason string `json:"reason"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading the Pod list: %w", err)
	}
	pods := make([]Pod, 0, len(list.Items))
	for _, it := range list.Items {
		p := Pod{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name,
			Finished: it.Status.Phase == "Succeeded" || it.Status.Phase == "Failed",
			Phase:    it.Status.Phase, Reason: it.Status.Reason}
		req, err := it.Spec.memoryRequest()
		if err != nil {
			return nil, fmt.Errorf("pod %s/%s: %w", p.Namespace, p.Name, err)
		}
		p.MemoryRequest = req
		_, p.Mirror = it.Metadata.Annotations[mirrorAnnotation]
		for _, o := range it.Metadata.OwnerReferences {
			p.DaemonSet = p.DaemonSet || o.Kind == "DaemonSet"
		}
		pods = append(pods, p)
	}
	return pods, nil
}

type container struct {
	RestartPolicy string `json:"restartPolicy"`
	Resources     struct {
		Requests map[string]string `json:"requests"`
	} `json:"resources"`
}

type podSpec struct {
	Containers     []container       `json:"containers"`
	InitContainers []container       `json:"initContainers"`
	Overhead       map[string]string `json:"overhead"`
}

// memoryRequest is the pod's effective memory request: the containers and
// the sidecars (init containers with restartPolicy Always) run together, a
// plain init container runs alone before them, with the sidecars started
// ahead of it.
func (s podSpec) memoryRequest() (resource.Quantity, error) {
	var total, sidecars, initPeak resource.Quantity
	for _, c := range s.InitContainers {
		q, err := memoryOf(c.Resources.Requests)
		if err != nil {
			return q, err
		}
		if c.RestartPolicy == "Always" {
			sidecars.Add(q)
			continue
		}
		q.Add(sidecars)
		if q.Cmp(initPeak) > 0 {
			initPeak = q
		}
	}
	total.Add(sidecars)
	for _, c := range s.Containers {
		q, err := memoryOf(c.Resources.Requests)
		if err != nil {
			return q, err
		}
		total.Add(q)
	}
	if initPeak.Cmp(total) > 0 {
		total = initPeak
	}
	o, err := memoryOf(s.Overhead)
	if err != nil {
		return o, err
	}
	total.Add(o)
	return total, nil
}

func memoryOf(m map[string]string) (resource.Quantity, error) {
	v, ok := m["memory"]
	if !ok {
		return resource.Quantity{}, nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return q, fmt.Errorf("memory %q: %w", v, err)
	}
	return q, nil
}

// Configz reads the running kubelet's configuration of a Node, through the
// API server's node proxy.
func (k Kubectl) Configz(ctx context.Context, node string) (string, error) {
	return k.Run(ctx, "get", "--raw", "/api/v1/nodes/"+node+"/proxy/configz")
}

// ConfigMapData reads one key of a ConfigMap's data.
func (k Kubectl) ConfigMapData(ctx context.Context, namespace, name, key string) (string, error) {
	out, err := k.Run(ctx, "-n", namespace, "get", "configmap", name, "-o", "json")
	if err != nil {
		return "", err
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &cm); err != nil {
		return "", fmt.Errorf("reading configmap %s/%s: %w", namespace, name, err)
	}
	v, ok := cm.Data[key]
	if !ok {
		return "", fmt.Errorf("configmap %s/%s has no key %s", namespace, name, key)
	}
	return v, nil
}

// PatchConfigMapData sets one key of a ConfigMap's data with a merge patch;
// every other key stays.
func (k Kubectl) PatchConfigMapData(ctx context.Context, namespace, name, key, value string) error {
	patch, err := json.Marshal(map[string]any{"data": map[string]string{key: value}})
	if err != nil {
		return err
	}
	_, err = k.Run(ctx, "-n", namespace, "patch", "configmap", name, "--type", "merge", "-p", string(patch))
	return err
}

// Database is a CloudNativePG Cluster as the database gate reads it.
type Database struct {
	Namespace string
	Name      string
	Instances int
	Ready     int
	Phase     string
}

// Healthy reports whether every instance the Cluster asks for is ready.
func (d Database) Healthy() bool { return d.Instances > 0 && d.Ready >= d.Instances }

// cnpgClusters is the CloudNativePG Cluster resource.
const cnpgClusters = "clusters.postgresql.cnpg.io"

// Databases reads every CloudNativePG Cluster. A cluster without the CNPG
// CRD has none: nil and no error.
func (k Kubectl) Databases(ctx context.Context) ([]Database, error) {
	crd, err := k.Run(ctx, "get", "crd", cnpgClusters, "--ignore-not-found", "-o", "name")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(crd) == "" {
		return nil, nil
	}
	out, err := k.Run(ctx, "get", cnpgClusters, "--all-namespaces", "-o", "json")
	if err != nil {
		return nil, err
	}
	return ParseDatabases([]byte(out))
}

// ParseDatabases reads `kubectl get clusters.postgresql.cnpg.io -o json`.
func ParseDatabases(b []byte) ([]Database, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Instances int `json:"instances"`
			} `json:"spec"`
			Status struct {
				ReadyInstances int    `json:"readyInstances"`
				Phase          string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading the CNPG Cluster list: %w", err)
	}
	dbs := make([]Database, 0, len(list.Items))
	for _, it := range list.Items {
		dbs = append(dbs, Database{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name,
			Instances: it.Spec.Instances, Ready: it.Status.ReadyInstances, Phase: it.Status.Phase})
	}
	return dbs, nil
}
