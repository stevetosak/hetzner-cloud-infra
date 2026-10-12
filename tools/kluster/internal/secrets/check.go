package secrets

import (
	"fmt"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sops"
)

// File is one encrypted file to check: its path relative to the repository
// root, with forward slashes, and its content.
type File struct {
	Path string
	Data []byte
}

// Check needs no age key and no Bitwarden. It refuses a file the catalog
// does not list and a file that is not fully encrypted. The catalog itself
// is checked when it is loaded. Errors name files and keys, never values.
func Check(cat *catalog.Catalog, files []File) []error {
	var errs []error
	for _, f := range files {
		if !strings.HasSuffix(f.Path, catalog.EncSuffix) {
			continue
		}
		if _, ok := cat.ByFile(f.Path); !ok {
			errs = append(errs, fmt.Errorf("%s: not listed in %s", f.Path, catalog.FileName))
		}
		if err := sops.CheckEncrypted(f.Data); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.Path, err))
		}
	}
	return errs
}

// Absent returns the catalog files that do not exist yet, as told by
// exists. They are not an error: generate or pull creates them.
func Absent(cat *catalog.Catalog, exists func(rel string) bool) []string {
	var out []string
	for _, s := range cat.Secrets {
		if !exists(s.File) {
			out = append(out, s.File)
		}
	}
	return out
}
