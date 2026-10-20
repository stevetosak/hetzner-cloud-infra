// Package fakevault is a password manager that holds a few items in memory,
// for tests that resolve an environment without rbw.
package fakevault

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
)

// Of answers rbw with one item per name; its fields are name=value pairs.
func Of(items map[string][]string) *bitwarden.Client {
	var names []string
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "unlocked", "sync":
			return nil, nil
		case "list":
			var rows []string
			for i, name := range names {
				rows = append(rows, `{"id":"id`+strconv.Itoa(i)+`","name":"`+name+`"}`)
			}
			return []byte("[" + strings.Join(rows, ",") + "]"), nil
		case "get":
			i, _ := strconv.Atoi(strings.TrimPrefix(args[2], "id"))
			var fs []string
			for _, f := range items[names[i]] {
				k, v, _ := strings.Cut(f, "=")
				fs = append(fs, `{"name":"`+k+`","value":"`+v+`"}`)
			}
			return []byte(`{"id":"` + args[2] + `","name":"` + names[i] + `","fields":[` + strings.Join(fs, ",") + `]}`), nil
		}
		return nil, errors.New("unexpected call")
	}
	noBw := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("bw is not used here") }
	return bitwarden.NewWithRunners(run, noBw, &bytes.Buffer{})
}
