// Package kubeconfig turns the admin.conf kubeadm writes into the entry the
// operator's laptop holds (docs/runbook/control-plane.md, step 6), and proves
// it answers. Every entity is renamed: a rebuilt cluster reuses the VPN
// address, so the address alone cannot tell a new cluster from a dead one.
package kubeconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// File is a kubeconfig with the fields kubeadm writes into admin.conf.
type File struct {
	APIVersion     string         `yaml:"apiVersion"`
	Kind           string         `yaml:"kind"`
	Clusters       []NamedCluster `yaml:"clusters"`
	Contexts       []NamedContext `yaml:"contexts"`
	CurrentContext string         `yaml:"current-context"`
	Users          []NamedUser    `yaml:"users"`
}

type NamedCluster struct {
	Name    string  `yaml:"name"`
	Cluster Cluster `yaml:"cluster"`
}

type Cluster struct {
	CAData string `yaml:"certificate-authority-data"`
	Server string `yaml:"server"`
}

type NamedContext struct {
	Name    string  `yaml:"name"`
	Context Context `yaml:"context"`
}

type Context struct {
	Cluster string `yaml:"cluster"`
	User    string `yaml:"user"`
}

type NamedUser struct {
	Name string `yaml:"name"`
	User User   `yaml:"user"`
}

type User struct {
	ClientCertData string `yaml:"client-certificate-data"`
	ClientKeyData  string `yaml:"client-key-data"`
}

// Names are the kubeconfig entries for one cluster name.
type Names struct{ Cluster, User, Context string }

// NamesFor is cluster <name>, user <name>-admin, context <name>-admin@<name>.
func NamesFor(cluster string) Names {
	return Names{Cluster: cluster, User: cluster + "-admin", Context: cluster + "-admin@" + cluster}
}

// Parse reads a single-cluster kubeconfig.
func Parse(data []byte) (*File, error) {
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	if len(f.Clusters) != 1 || len(f.Users) != 1 || len(f.Contexts) != 1 {
		return nil, fmt.Errorf("want one cluster, user and context, got %d, %d, %d", len(f.Clusters), len(f.Users), len(f.Contexts))
	}
	return &f, nil
}

// Entries is what a kubeconfig of any size holds under one set of names.
type Entries struct {
	Cluster        *Cluster // nil if absent
	HasUser        bool
	HasContext     bool
	CurrentContext string
}

// Find reads the entries named n from a kubeconfig that may hold many
// clusters, such as the operator's ~/.kube/config.
func Find(data []byte, n Names) (Entries, error) {
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return Entries{}, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	e := Entries{CurrentContext: f.CurrentContext}
	for i := range f.Clusters {
		if f.Clusters[i].Name == n.Cluster {
			e.Cluster = &f.Clusters[i].Cluster
		}
	}
	for _, u := range f.Users {
		e.HasUser = e.HasUser || u.Name == n.User
	}
	for _, c := range f.Contexts {
		e.HasContext = e.HasContext || c.Name == n.Context
	}
	return e, nil
}

// ForLaptop renames admin.conf to n and points it at server: kubeadm writes
// the Endpoint name, which the laptop deliberately does not resolve, so the
// laptop uses the VPN address in the certificate (ADR 0006).
func ForLaptop(adminConf []byte, n Names, server string) ([]byte, error) {
	f, err := Parse(adminConf)
	if err != nil {
		return nil, err
	}
	f.Clusters[0].Name = n.Cluster
	f.Clusters[0].Cluster.Server = server
	f.Users[0].Name = n.User
	f.Contexts[0] = NamedContext{Name: n.Context, Context: Context{Cluster: n.Cluster, User: n.User}}
	f.CurrentContext = n.Context
	return yaml.Marshal(f)
}

// DialFunc opens a network connection.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Readyz asks the API server in kubeconfig for /readyz with its credentials,
// through dial, and requires `ok`. It is the runbook's
// `kubectl get --raw=/readyz`, run where kubectl cannot reach.
func Readyz(ctx context.Context, kubeconfig []byte, dial DialFunc) error {
	f, err := Parse(kubeconfig)
	if err != nil {
		return err
	}
	ca, err := base64.StdEncoding.DecodeString(f.Clusters[0].Cluster.CAData)
	if err != nil {
		return fmt.Errorf("certificate-authority-data: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("certificate-authority-data holds no certificate")
	}
	certPEM, err := base64.StdEncoding.DecodeString(f.Users[0].User.ClientCertData)
	if err != nil {
		return fmt.Errorf("client-certificate-data: %w", err)
	}
	keyPEM, err := base64.StdEncoding.DecodeString(f.Users[0].User.ClientKeyData)
	if err != nil {
		return fmt.Errorf("client-key-data: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("client certificate: %w", err)
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:     dial,
			TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		},
	}
	url := strings.TrimRight(f.Clusters[0].Cluster.Server, "/") + "/readyz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		return fmt.Errorf("GET %s: %s %q", url, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
