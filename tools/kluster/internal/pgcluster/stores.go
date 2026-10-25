package pgcluster

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kbuild"
)

// The builds the ObjectStores come from: what core-cnpg syncs in each
// environment. A rehearsal's archive store points at a bucket of its own.
const (
	StoresDir          = "core/cnpg"
	RehearsalStoresDir = "core/rehearsal/cnpg"
)

// Store is one ObjectStore as kluster reads it: the bucket, the R2 endpoint
// and the Secret keys of its token.
type Store struct {
	Name     string
	Bucket   string
	Endpoint string
	// Secret is the token's Secret in Namespace; AccessKey and SecretKey
	// are its two keys.
	Secret, AccessKey, SecretKey string
}

// Account is the Cloudflare account id: the first label of the endpoint's
// host (<account>.r2.cloudflarestorage.com). It is not a secret.
func (s Store) Account() string {
	u, _ := url.Parse(s.Endpoint)
	account, _, _ := strings.Cut(u.Hostname(), ".")
	return account
}

type objectStore struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Configuration struct {
			DestinationPath string `yaml:"destinationPath"`
			EndpointURL     string `yaml:"endpointURL"`
			S3Credentials   struct {
				AccessKeyID     secretKeyRef `yaml:"accessKeyId"`
				SecretAccessKey secretKeyRef `yaml:"secretAccessKey"`
			} `yaml:"s3Credentials"`
		} `yaml:"configuration"`
	} `yaml:"spec"`
}

type secretKeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

// Stores reads the archive store and the source store from the build of
// StoresDir (live) or RehearsalStoresDir, under root.
func Stores(root string, live bool) (archive, source Store, err error) {
	dir := RehearsalStoresDir
	if live {
		dir = StoresDir
	}
	m, err := kbuild.Build(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return Store{}, Store{}, err
	}
	found := map[string]Store{}
	for _, r := range m.Resources() {
		if r.GetKind() != "ObjectStore" {
			continue
		}
		b, err := r.AsYAML()
		if err != nil {
			return Store{}, Store{}, err
		}
		var o objectStore
		if err := yaml.Unmarshal(b, &o); err != nil {
			return Store{}, Store{}, fmt.Errorf("%s: ObjectStore %s: %w", dir, r.GetName(), err)
		}
		s, err := o.store()
		if err != nil {
			return Store{}, Store{}, fmt.Errorf("%s: %w", dir, err)
		}
		found[s.Name] = s
	}
	archive, ok := found[ArchiveStore]
	if !ok {
		return Store{}, Store{}, fmt.Errorf("%s builds no ObjectStore %s", dir, ArchiveStore)
	}
	source, ok = found[SourceStore]
	if !ok {
		return Store{}, Store{}, fmt.Errorf("%s builds no ObjectStore %s", dir, SourceStore)
	}
	return archive, source, nil
}

func (o objectStore) store() (Store, error) {
	c := o.Spec.Configuration
	name := o.Metadata.Name
	path, ok := strings.CutPrefix(c.DestinationPath, "s3://")
	bucket := strings.TrimSuffix(path, "/")
	if !ok || bucket == "" || strings.Contains(bucket, "/") {
		return Store{}, fmt.Errorf("ObjectStore %s: destinationPath %q is not s3://<bucket>/", name, c.DestinationPath)
	}
	if u, err := url.Parse(c.EndpointURL); err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return Store{}, fmt.Errorf("ObjectStore %s: endpointURL %q is not https://<host>", name, c.EndpointURL)
	}
	id, secret := c.S3Credentials.AccessKeyID, c.S3Credentials.SecretAccessKey
	if id.Name == "" || id.Name != secret.Name || id.Key == "" || secret.Key == "" {
		return Store{}, fmt.Errorf("ObjectStore %s: both S3 keys must come from one Secret", name)
	}
	return Store{Name: name, Bucket: bucket, Endpoint: c.EndpointURL,
		Secret: id.Name, AccessKey: id.Key, SecretKey: secret.Key}, nil
}
