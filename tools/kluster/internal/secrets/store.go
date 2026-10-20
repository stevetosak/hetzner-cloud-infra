package secrets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sops"
)

// Cipher encrypts and decrypts one file's content. *sops.Binary is one.
type Cipher interface {
	Encrypt(ctx context.Context, plaintext []byte, file string) ([]byte, error)
	Decrypt(ctx context.Context, ciphertext []byte, file, ageKey string) ([]byte, error)
}

// AgeKey returns the age private key. The Store asks for it once, at the
// first decrypt, so a command that only encrypts never needs it.
type AgeKey func(ctx context.Context) (string, error)

// Store reads and writes the encrypted files the catalog lists.
type Store struct {
	cat    *catalog.Catalog
	cipher Cipher
	ageKey AgeKey

	once sync.Once
	key  string
	err  error
}

// NewStore returns a Store over the files of cat.
func NewStore(cat *catalog.Catalog, cipher Cipher, ageKey AgeKey) *Store {
	return &Store{cat: cat, cipher: cipher, ageKey: ageKey}
}

// Catalog is the catalog the Store serves.
func (st *Store) Catalog() *catalog.Catalog { return st.cat }

func (st *Store) privateKey(ctx context.Context) (string, error) {
	st.once.Do(func() { st.key, st.err = st.ageKey(ctx) })
	return st.key, st.err
}

// Read decrypts the file of s. A file that does not exist yet is not an
// error: it returns nil values and found false. A file whose name, type or
// labels differ from the catalog returns its values and a mismatch flag, so
// a write can put it right.
func (st *Store) Read(ctx context.Context, s *catalog.Secret) (v Values, found, mismatch bool, err error) {
	cipher, err := os.ReadFile(st.cat.Path(s))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	key, err := st.privateKey(ctx)
	if err != nil {
		return nil, false, false, err
	}
	plain, err := st.cipher.Decrypt(ctx, cipher, s.File, key)
	if err != nil {
		return nil, false, false, fmt.Errorf("%s: %w", s.File, err)
	}
	sh, v, err := parse(plain)
	if err != nil {
		return nil, false, false, fmt.Errorf("%s: %w", s.File, err)
	}
	if sh.ref != s.Ref {
		return nil, false, false, fmt.Errorf("%s holds %s, but the catalog lists it as %s", s.File, sh.ref, s.Ref)
	}
	return v, true, !sh.matches(s), nil
}

// Write encrypts v as the whole file of s and replaces the file in one
// rename. It refuses to write a result that is not fully encrypted.
func (st *Store) Write(ctx context.Context, s *catalog.Secret, v Values) error {
	plain, err := render(s, v)
	if err != nil {
		return fmt.Errorf("%s: %w", s.File, err)
	}
	cipher, err := st.cipher.Encrypt(ctx, plain, s.File)
	if err != nil {
		return fmt.Errorf("%s: %w", s.File, err)
	}
	if err := sops.CheckEncrypted(cipher); err != nil {
		return fmt.Errorf("%s: sops output refused: %w", s.File, err)
	}
	return writeAtomic(st.cat.Path(s), cipher)
}

// writeAtomic writes data to a temporary file beside path and renames it
// over path, so an interrupted write never leaves half a file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
