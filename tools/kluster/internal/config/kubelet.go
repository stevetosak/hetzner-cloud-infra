package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Kubelet is what each node's kubelet keeps back for itself and the system,
// and when it starts to evict (ADR 0014). kluster writes these fields into
// the cluster's kubelet-config ConfigMap, which kubeadm gives every node it
// joins; `kluster node kubelet-config` brings one running node to them.
// Every field is a KubeletConfiguration field of the same name.
type Kubelet struct {
	KubeReserved              map[string]string `yaml:"kubeReserved"`
	SystemReserved            map[string]string `yaml:"systemReserved"`
	EvictionSoft              map[string]string `yaml:"evictionSoft"`
	EvictionSoftGracePeriod   map[string]string `yaml:"evictionSoftGracePeriod"`
	EvictionMaxPodGracePeriod int               `yaml:"evictionMaxPodGracePeriod"`
}

// Fields returns the fields kluster.yaml sets, by their KubeletConfiguration
// name. A field left out is not touched on the node.
func (k Kubelet) Fields() map[string]any {
	f := map[string]any{}
	for name, m := range map[string]map[string]string{
		"kubeReserved":            k.KubeReserved,
		"systemReserved":          k.SystemReserved,
		"evictionSoft":            k.EvictionSoft,
		"evictionSoftGracePeriod": k.EvictionSoftGracePeriod,
	} {
		if len(m) > 0 {
			f[name] = m
		}
	}
	if k.EvictionMaxPodGracePeriod > 0 {
		f["evictionMaxPodGracePeriod"] = k.EvictionMaxPodGracePeriod
	}
	return f
}

// reservable are the resources kubeReserved and systemReserved take.
var reservable = map[string]bool{"cpu": true, "memory": true, "ephemeral-storage": true, "pid": true}

func (k Kubelet) validate() error {
	var errs []error
	for _, r := range []struct {
		field string
		m     map[string]string
	}{{"kubelet.kubeReserved", k.KubeReserved}, {"kubelet.systemReserved", k.SystemReserved}} {
		for _, name := range sortedKeys(r.m) {
			if !reservable[name] {
				errs = append(errs, fmt.Errorf("%s.%s: not a resource the kubelet reserves (cpu, memory, ephemeral-storage, pid)", r.field, name))
				continue
			}
			if _, err := resource.ParseQuantity(r.m[name]); err != nil {
				errs = append(errs, fmt.Errorf("%s.%s: %q is not a quantity", r.field, name, r.m[name]))
			}
		}
	}
	for _, signal := range sortedKeys(k.EvictionSoft) {
		if err := thresholdValue(k.EvictionSoft[signal]); err != nil {
			errs = append(errs, fmt.Errorf("kubelet.evictionSoft.%s: %w", signal, err))
		}
		// A soft threshold without a grace period stops the kubelet at its
		// start: it is refused here instead.
		if _, ok := k.EvictionSoftGracePeriod[signal]; !ok {
			errs = append(errs, fmt.Errorf("kubelet.evictionSoft.%s needs kubelet.evictionSoftGracePeriod.%s: the kubelet does not start without it", signal, signal))
		}
	}
	for _, signal := range sortedKeys(k.EvictionSoftGracePeriod) {
		if _, ok := k.EvictionSoft[signal]; !ok {
			errs = append(errs, fmt.Errorf("kubelet.evictionSoftGracePeriod.%s has no kubelet.evictionSoft.%s", signal, signal))
		}
		if d, err := time.ParseDuration(k.EvictionSoftGracePeriod[signal]); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("kubelet.evictionSoftGracePeriod.%s: %q is not a positive duration", signal, k.EvictionSoftGracePeriod[signal]))
		}
	}
	if k.EvictionMaxPodGracePeriod < 0 {
		errs = append(errs, errors.New("kubelet.evictionMaxPodGracePeriod must not be negative"))
	}
	return errors.Join(errs...)
}

// thresholdValue accepts an eviction threshold: a quantity or a percentage.
func thresholdValue(v string) error {
	if p, ok := strings.CutSuffix(v, "%"); ok {
		var f float64
		if _, err := fmt.Sscanf(p, "%g", &f); err != nil || f <= 0 || f >= 100 {
			return fmt.Errorf("%q is not a percentage between 0 and 100", v)
		}
		return nil
	}
	if _, err := resource.ParseQuantity(v); err != nil {
		return fmt.Errorf("%q is neither a quantity nor a percentage", v)
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
