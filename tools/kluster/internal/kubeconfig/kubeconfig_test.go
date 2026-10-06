package kubeconfig

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type pair struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
	kpem []byte
}

func issue(t *testing.T, tmpl *x509.Certificate, parent *pair) *pair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, parentCert := key, tmpl
	if parent != nil {
		signer, parentCert = parent.key, parent.cert
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parentCert, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	kder, _ := x509.MarshalECPrivateKey(key)
	return &pair{cert, key,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})}
}

// apiServer is a TLS server that requires a client certificate from ca and
// answers /readyz, like kube-apiserver. It returns an admin.conf for it that
// names the Endpoint, as kubeadm writes it.
func apiServer(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	now := time.Now()
	ca := issue(t, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kubernetes"},
		NotBefore: now, NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}, nil)
	srv := issue(t, &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "kube-apiserver"},
		NotBefore: now, NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("10.100.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, ca)
	admin := issue(t, &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "kubernetes-admin"},
		NotBefore: now, NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, ca)

	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			fmt.Fprint(w, "ok")
			return
		}
		http.NotFound(w, r)
	}))
	srvCert, _ := tls.X509KeyPair(srv.pem, srv.kpem)
	s.TLS = &tls.Config{Certificates: []tls.Certificate{srvCert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}
	s.StartTLS()
	t.Cleanup(s.Close)

	b64 := base64.StdEncoding.EncodeToString
	conf := `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: ` + b64(ca.pem) + `
    server: https://k8s-cp.tosak.internal:6443
  name: kubernetes
contexts:
- context:
    cluster: kubernetes
    user: kubernetes-admin
  name: kubernetes-admin@kubernetes
current-context: kubernetes-admin@kubernetes
kind: Config
users:
- name: kubernetes-admin
  user:
    client-certificate-data: ` + b64(admin.pem) + `
    client-key-data: ` + b64(admin.kpem) + `
`
	return s, []byte(conf)
}

func TestForLaptopRenamesEveryEntity(t *testing.T) {
	_, admin := apiServer(t)
	out, err := ForLaptop(admin, NamesFor("tosak"), "https://10.100.0.1:6443")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if f.Clusters[0].Name != "tosak" || f.Users[0].Name != "tosak-admin" || f.Contexts[0].Name != "tosak-admin@tosak" ||
		f.CurrentContext != "tosak-admin@tosak" || f.Contexts[0].Context != (Context{"tosak", "tosak-admin"}) {
		t.Fatalf("not renamed:\n%s", out)
	}
	if f.Clusters[0].Cluster.Server != "https://10.100.0.1:6443" {
		t.Fatalf("server not pointed at the VPN address: %s", f.Clusters[0].Cluster.Server)
	}
	if strings.Contains(string(out), "kubernetes-admin") {
		t.Fatalf("an old name survived:\n%s", out)
	}
}

// The kubeconfig names 10.100.0.1, and the dialer carries it to the test
// server, as the tunnel carries it to the hub.
func TestReadyzThroughADialer(t *testing.T) {
	s, admin := apiServer(t)
	conf, err := ForLaptop(admin, NamesFor("tosak"), "https://10.100.0.1:6443")
	if err != nil {
		t.Fatal(err)
	}
	target := s.Listener.Addr().String()
	var dialed string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}
	if err := Readyz(context.Background(), conf, dial); err != nil {
		t.Fatal(err)
	}
	if dialed != "10.100.0.1:6443" {
		t.Fatalf("dialed %s, want the VPN address", dialed)
	}
}

func TestReadyzRefusesAnotherClustersCA(t *testing.T) {
	s, _ := apiServer(t)
	_, other := apiServer(t)
	conf, _ := ForLaptop(other, NamesFor("tosak"), "https://10.100.0.1:6443")
	target := s.Listener.Addr().String()
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}
	if err := Readyz(context.Background(), conf, dial); err == nil {
		t.Fatal("a kubeconfig of another cluster passed /readyz")
	}
}
