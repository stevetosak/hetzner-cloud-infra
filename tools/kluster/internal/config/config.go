// Package config loads and validates kluster.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Module names. Each is one Terraform root Module with its own state
// (ADR 0002).
const (
	ModuleShared       = "shared"
	ModuleControlPlane = "control-plane"
	ModuleWorkers      = "workers"
)

// Environment names (ADR 0009).
const (
	EnvLive      = "live"
	EnvRehearsal = "rehearsal"
)

// Config is the root of kluster.yaml.
type Config struct {
	ControlPlane ControlPlane   `yaml:"controlPlane"`
	Cluster      Cluster        `yaml:"cluster"`
	Node         Node           `yaml:"node"`
	Workers      Workers        `yaml:"workers"`
	WireGuard    WireGuard      `yaml:"wireguard"`
	Versions     Versions       `yaml:"versions"`
	Manifests    Manifests      `yaml:"manifests"`
	Terraform    Terraform      `yaml:"terraform"`
	Envs         map[string]Env `yaml:"envs"`
	SSH          SSH            `yaml:"ssh"`

	// dir is the directory kluster.yaml was read from. Module paths are
	// relative to it.
	dir string
}

type ControlPlane struct {
	// Name is the Hetzner server name. `cp init` creates it only into an
	// empty slot (ADR 0009).
	Name      string `yaml:"name"`
	PrivateIP string `yaml:"privateIP"`
	VpnIP     string `yaml:"vpnIP"`
	Endpoint  string `yaml:"endpoint"`
	SSHUser   string `yaml:"sshUser"`
}

// Cluster is what kubeadm and the cloud controller are told about the
// cluster's networks (ADR 0001, ADR 0006).
type Cluster struct {
	PodCIDR       string `yaml:"podCIDR"`
	ServiceCIDR   string `yaml:"serviceCIDR"`
	HcloudNetwork string `yaml:"hcloudNetwork"`
	// PodMTU is what each pod interface and flannel.1 get (ADR 0001). The
	// flannel manifest carries the underlay MTU instead.
	PodMTU int `yaml:"podMTU"`
}

type Node struct {
	SSHUser          string `yaml:"sshUser"`
	User             string `yaml:"user"`
	NetworkInterface string `yaml:"networkInterface"`
}

// Workers is how `node add` fills a new Worker's entry in the Worker set. A
// new Worker takes the lowest free address of each pool.
type Workers struct {
	ServerType string `yaml:"serverType"`
	// PrivateIPs and VpnIPs are inclusive ranges, "first-last".
	PrivateIPs string `yaml:"privateIPs"`
	VpnIPs     string `yaml:"vpnIPs"`
}

type WireGuard struct {
	Subnet string `yaml:"subnet"`
	Port   int    `yaml:"port"`
	// ProbeIP is the VPN address of kluster's own short-lived peer, the one
	// that proves the operator route through the hub.
	ProbeIP string `yaml:"probeIP"`
	Peers   []Peer `yaml:"peers"`
}

type Peer struct {
	Name       string `yaml:"name"`
	PublicKey  string `yaml:"publicKey"`
	AllowedIPs string `yaml:"allowedIPs"`
}

type Versions struct {
	Containerd string `yaml:"containerd"`
	Runc       string `yaml:"runc"`
	CNIPlugins string `yaml:"cniPlugins"`
	Kubernetes string `yaml:"kubernetes"`
}

// Manifests are the cluster manifests the Control Plane Stages apply, as
// paths relative to kluster.yaml.
type Manifests struct {
	Flannel string `yaml:"flannel"`
	CCM     string `yaml:"ccm"`
	CSI     string `yaml:"csi"`
}

type Terraform struct {
	Modules map[string]string `yaml:"modules"`
}

// Env names the environment variables that hold one environment's
// credentials. It never holds a value.
type Env struct {
	HcloudTokenEnv       string `yaml:"hcloudTokenEnv"`
	R2AccessKeyIDEnv     string `yaml:"r2AccessKeyIDEnv"`
	R2SecretAccessKeyEnv string `yaml:"r2SecretAccessKeyEnv"`
	StateBucket          string `yaml:"stateBucket"`
	RefuseProjectWithIP  string `yaml:"refuseProjectWithIP"`
	// WorkerSet is the tfvars file that declares this environment's
	// Workers, relative to kluster.yaml. kluster passes it to the workers
	// Module with -var-file, so it wins over terraform.tfvars, which
	// Terraform loads in every environment.
	WorkerSet string `yaml:"workerSet"`
	Laptop    Laptop `yaml:"laptop"`
}

// Laptop is what kluster edits on the operator's own machine after a
// Control Plane build: the WireGuard config that points at the hub, and the
// kubeconfig. A leading ~ in a path is the operator's home.
type Laptop struct {
	WireGuardConf string `yaml:"wireguardConf"`
	// WireGuardUnit is restarted after an edit. Empty: nothing is restarted.
	WireGuardUnit string `yaml:"wireguardUnit"`
	// Sudo reads and writes WireGuardConf through sudo, which asks the
	// operator for a password on the terminal.
	Sudo       bool   `yaml:"sudo"`
	Kubeconfig string `yaml:"kubeconfig"`
	// MergeKubeconfig merges into Kubeconfig and switches to the new
	// context. Without it, Kubeconfig is written whole and holds the new
	// cluster alone.
	MergeKubeconfig bool `yaml:"mergeKubeconfig"`
	// ClusterName names the kubeconfig entries: cluster <name>, user
	// <name>-admin, context <name>-admin@<name>.
	ClusterName string `yaml:"clusterName"`
}

type SSH struct {
	IdentityFile string `yaml:"identityFile"`
}

// Load reads and validates the kluster.yaml file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	cfg.dir = filepath.Dir(abs)

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

// ModuleDir returns the absolute directory of the named Terraform Module.
func (c *Config) ModuleDir(name string) (string, error) {
	rel, ok := c.Terraform.Modules[name]
	if !ok {
		return "", fmt.Errorf("unknown Terraform Module %q (known: %v)", name, c.moduleNames())
	}
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	return filepath.Join(c.dir, rel), nil
}

// Path resolves a path from kluster.yaml: relative to the file, or under
// the operator's home for a leading ~.
func (c *Config) Path(p string) (string, error) {
	switch {
	case p == "":
		return "", errors.New("empty path")
	case p == "~" || strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	case filepath.IsAbs(p):
		return p, nil
	}
	return filepath.Join(c.dir, p), nil
}

// Env returns the named environment.
func (c *Config) Env(name string) (Env, error) {
	e, ok := c.Envs[name]
	if !ok {
		return Env{}, fmt.Errorf("unknown env %q", name)
	}
	return e, nil
}

func (c *Config) moduleNames() []string {
	names := make([]string, 0, len(c.Terraform.Modules))
	for n := range c.Terraform.Modules {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) validate() error {
	var errs []error
	for _, m := range []string{ModuleShared, ModuleControlPlane, ModuleWorkers} {
		if c.Terraform.Modules[m] == "" {
			errs = append(errs, fmt.Errorf("terraform.modules.%s is required", m))
		}
	}
	required := []struct{ field, v string }{
		{"controlPlane.name", c.ControlPlane.Name},
		{"controlPlane.privateIP", c.ControlPlane.PrivateIP},
		{"controlPlane.vpnIP", c.ControlPlane.VpnIP},
		{"controlPlane.endpoint", c.ControlPlane.Endpoint},
		{"controlPlane.sshUser", c.ControlPlane.SSHUser},
		{"cluster.podCIDR", c.Cluster.PodCIDR},
		{"cluster.serviceCIDR", c.Cluster.ServiceCIDR},
		{"cluster.hcloudNetwork", c.Cluster.HcloudNetwork},
		{"wireguard.subnet", c.WireGuard.Subnet},
		{"wireguard.probeIP", c.WireGuard.ProbeIP},
		{"manifests.flannel", c.Manifests.Flannel},
		{"manifests.ccm", c.Manifests.CCM},
		{"manifests.csi", c.Manifests.CSI},
		{"workers.serverType", c.Workers.ServerType},
		{"workers.privateIPs", c.Workers.PrivateIPs},
		{"workers.vpnIPs", c.Workers.VpnIPs},
	}
	for _, r := range required {
		if r.v == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.field))
		}
	}
	if c.Cluster.PodMTU <= 0 || c.WireGuard.Port <= 0 {
		errs = append(errs, errors.New("cluster.podMTU and wireguard.port are required"))
	}
	for _, name := range []string{EnvLive, EnvRehearsal} {
		e, ok := c.Envs[name]
		if !ok {
			errs = append(errs, fmt.Errorf("envs.%s is required", name))
			continue
		}
		if e.HcloudTokenEnv == "" || e.R2AccessKeyIDEnv == "" || e.R2SecretAccessKeyEnv == "" {
			errs = append(errs, fmt.Errorf("envs.%s must name hcloudTokenEnv, r2AccessKeyIDEnv and r2SecretAccessKeyEnv", name))
		}
		if e.WorkerSet == "" {
			errs = append(errs, fmt.Errorf("envs.%s.workerSet is required", name))
		}
		if e.Laptop.WireGuardConf == "" || e.Laptop.Kubeconfig == "" || e.Laptop.ClusterName == "" {
			errs = append(errs, fmt.Errorf("envs.%s.laptop must name wireguardConf, kubeconfig and clusterName", name))
		}
	}
	if r, ok := c.Envs[EnvRehearsal]; ok {
		// A rehearsal that shares the live bucket or credentials is not
		// isolated, whatever the code does with the keys (ADR 0009).
		if r.StateBucket == "" {
			errs = append(errs, errors.New("envs.rehearsal.stateBucket is required: rehearsal state never shares the live bucket"))
		}
		if r.RefuseProjectWithIP == "" {
			errs = append(errs, errors.New("envs.rehearsal.refuseProjectWithIP is required"))
		}
		if l, ok := c.Envs[EnvLive]; ok {
			if r.HcloudTokenEnv == l.HcloudTokenEnv || r.R2AccessKeyIDEnv == l.R2AccessKeyIDEnv ||
				r.R2SecretAccessKeyEnv == l.R2SecretAccessKeyEnv || r.StateBucket == l.StateBucket {
				errs = append(errs, errors.New("envs.rehearsal must name its own token, R2 keys and bucket, not the live ones"))
			}
			// The same file would make `node add --env rehearsal` write the
			// live Worker set.
			if r.WorkerSet == l.WorkerSet {
				errs = append(errs, errors.New("envs.rehearsal.workerSet must name its own file, not the live Worker set"))
			}
			// A rehearsal Control Plane has the live VPN addresses. Writing
			// it into the operator's live WireGuard config or kubeconfig
			// would cut them off from the live cluster.
			rl, ll := r.Laptop, l.Laptop
			if rl.WireGuardConf == ll.WireGuardConf || rl.Kubeconfig == ll.Kubeconfig || rl.ClusterName == ll.ClusterName {
				errs = append(errs, errors.New("envs.rehearsal.laptop must name its own wireguardConf, kubeconfig and clusterName, not the live ones"))
			}
		}
		if r.Laptop.Sudo || r.Laptop.MergeKubeconfig || r.Laptop.WireGuardUnit != "" {
			errs = append(errs, errors.New("envs.rehearsal.laptop must not use sudo, merge a kubeconfig or restart a unit: a rehearsal never touches the operator's live setup"))
		}
	}
	return errors.Join(errs...)
}
