package secrets

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// The age private key that decrypts every file lives in this Bitwarden
// item and field (ADR 0003, 2026-10-06 Amendment).
const (
	AgeKeyItem  = "kluster/age-key"
	AgeKeyField = "age_key"
)

// Vault is where Issued values live.
type Vault interface {
	// Issued returns the value of an issued key.
	Issued(ctx context.Context, k *catalog.Key) ([]byte, error)
}

// ErrSkipped means a Vault did not read the value on purpose: an attachment
// read with SkipAttachments set.
var ErrSkipped = errors.New("not read")

// Bitwarden is the Vault over the password manager. It reads each item
// once.
type Bitwarden struct {
	c     *bitwarden.Client
	items map[string]*bitwarden.Item
	// SkipAttachments makes Issued return ErrSkipped for an attachment, once
	// its item is found: an attachment is read through bw, about 6 s.
	SkipAttachments bool
}

// NewBitwarden returns the Vault over c.
func NewBitwarden(c *bitwarden.Client) *Bitwarden {
	return &Bitwarden{c: c, items: map[string]*bitwarden.Item{}}
}

func (b *Bitwarden) item(ctx context.Context, name string) (*bitwarden.Item, error) {
	if it, ok := b.items[name]; ok {
		return it, nil
	}
	it, err := b.c.Item(ctx, name)
	if err != nil {
		return nil, err
	}
	b.items[name] = it
	return it, nil
}

// Issued reads the key's field or attachment.
func (b *Bitwarden) Issued(ctx context.Context, k *catalog.Key) ([]byte, error) {
	if k.Kind != catalog.Issued {
		return nil, fmt.Errorf("a %s key has no value in Bitwarden", k.Kind)
	}
	it, err := b.item(ctx, k.Item)
	if err != nil {
		return nil, err
	}
	if k.Attachment != "" {
		if b.SkipAttachments {
			return nil, ErrSkipped
		}
		return b.c.Attachment(ctx, it, k.Attachment)
	}
	v, err := it.Field(k.Field)
	if err != nil {
		return nil, err
	}
	return []byte(v), nil
}

// AgeKey reads the age private key. It is an AgeKey for NewStore.
func (b *Bitwarden) AgeKey(ctx context.Context) (string, error) {
	it, err := b.item(ctx, AgeKeyItem)
	if err != nil {
		return "", err
	}
	return it.Field(AgeKeyField)
}

// IssuedSource names where an issued key's value lives, for output.
func IssuedSource(k *catalog.Key) string {
	if k.Attachment != "" {
		return k.Item + " attachment " + k.Attachment
	}
	return k.Item + " field " + k.Field
}

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Random returns n alphanumeric characters from crypto/rand, each drawn
// uniformly. Alphanumeric values need no quoting in a URL, a shell or YAML.
func Random(n int) ([]byte, error) {
	if n <= 0 {
		return nil, errors.New("a generated value needs a positive length")
	}
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		j, err := rand.Int(rand.Reader, max)
		if err != nil {
			return nil, err
		}
		out[i] = alphabet[j.Int64()]
	}
	return out, nil
}
