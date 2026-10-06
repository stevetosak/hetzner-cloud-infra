// Package kube runs kubectl on the Control Plane with its admin kubeconfig.
// kluster does not use a kubeconfig on the laptop: in a rehearsal it points
// at 10.100.0.1, which the laptop's WireGuard routes to the LIVE hub.
package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading the Node list: %w", err)
	}
	nodes := make([]Node, 0, len(list.Items))
	for _, it := range list.Items {
		n := Node{Name: it.Metadata.Name, Kubelet: it.Status.NodeInfo.KubeletVersion, ProviderID: it.Spec.ProviderID,
			Unschedulable: it.Spec.Unschedulable}
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
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading the Pod list: %w", err)
	}
	pods := make([]Pod, 0, len(list.Items))
	for _, it := range list.Items {
		p := Pod{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name,
			Finished: it.Status.Phase == "Succeeded" || it.Status.Phase == "Failed"}
		_, p.Mirror = it.Metadata.Annotations[mirrorAnnotation]
		for _, o := range it.Metadata.OwnerReferences {
			p.DaemonSet = p.DaemonSet || o.Kind == "DaemonSet"
		}
		pods = append(pods, p)
	}
	return pods, nil
}
