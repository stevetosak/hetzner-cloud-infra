// Package terraform wraps the Terraform CLI (via terraform-exec) and reads/
// writes the `workers` map in terraform.tfvars, which is the single source
// of truth for cluster worker nodes.
package terraform

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

// Worker mirrors the `workers` map object type declared in
// infra/variables.tf: map(object({ private_ip, server_type, labels })).
type Worker struct {
	PrivateIP  string
	ServerType string
	Labels     map[string]string
}

// ReadWorkers parses the `workers` attribute out of a terraform.tfvars file.
func ReadWorkers(path string) (map[string]Worker, error) {
	parser := hclparse.NewParser()
	file, diags := parser.ParseHCLFile(path)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing %s: %w", path, diags)
	}

	attrs, diags := file.Body.JustAttributes()
	if diags.HasErrors() {
		return nil, fmt.Errorf("reading attributes in %s: %w", path, diags)
	}

	attr, ok := attrs["workers"]
	if !ok {
		return map[string]Worker{}, nil
	}

	val, diags := attr.Expr.Value(nil)
	if diags.HasErrors() {
		return nil, fmt.Errorf("evaluating workers attribute in %s: %w", path, diags)
	}

	return ctyToWorkers(val)
}

// AddWorker appends a new entry to the workers map in path and writes the
// file back. It errors if name already exists.
func AddWorker(path, name string, w Worker) error {
	workers, err := ReadWorkers(path)
	if err != nil {
		return err
	}
	if _, exists := workers[name]; exists {
		return fmt.Errorf("worker %q already exists in %s", name, path)
	}
	workers[name] = w
	return writeWorkers(path, workers)
}

// RemoveWorker deletes an entry from the workers map in path and writes the
// file back. It errors if name does not exist.
func RemoveWorker(path, name string) error {
	workers, err := ReadWorkers(path)
	if err != nil {
		return err
	}
	if _, exists := workers[name]; !exists {
		return fmt.Errorf("worker %q not found in %s", name, path)
	}
	delete(workers, name)
	return writeWorkers(path, workers)
}

func writeWorkers(path string, workers map[string]Worker) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s for write: %w", path, err)
	}
	wf, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return fmt.Errorf("parsing %s for write: %w", path, diags)
	}

	body := wf.Body()
	if len(workers) == 0 {
		body.SetAttributeValue("workers", cty.EmptyObjectVal)
	} else {
		body.SetAttributeValue("workers", workersToCty(workers))
	}

	return writeFileAtomic(path, wf.Bytes())
}

func workersToCty(workers map[string]Worker) cty.Value {
	vals := make(map[string]cty.Value, len(workers))
	for name, w := range workers {
		labelVals := make(map[string]cty.Value, len(w.Labels))
		for k, v := range w.Labels {
			labelVals[k] = cty.StringVal(v)
		}
		vals[name] = cty.ObjectVal(map[string]cty.Value{
			"private_ip":  cty.StringVal(w.PrivateIP),
			"server_type": cty.StringVal(w.ServerType),
			"labels":      cty.MapVal(nonEmptyOrPlaceholder(labelVals)),
		})
	}
	return cty.ObjectVal(vals)
}

// nonEmptyOrPlaceholder guards against cty.MapVal panicking on an empty map
// (cty requires at least one element to infer the map's element type).
func nonEmptyOrPlaceholder(m map[string]cty.Value) map[string]cty.Value {
	if len(m) == 0 {
		return map[string]cty.Value{"role": cty.StringVal("worker")}
	}
	return m
}

func ctyToWorkers(val cty.Value) (map[string]Worker, error) {
	result := make(map[string]Worker)
	if val.IsNull() {
		return result, nil
	}
	it := val.ElementIterator()
	for it.Next() {
		key, elem := it.Element()
		w, err := ctyToWorker(elem)
		if err != nil {
			return nil, fmt.Errorf("worker %q: %w", key.AsString(), err)
		}
		result[key.AsString()] = w
	}
	return result, nil
}

func ctyToWorker(val cty.Value) (Worker, error) {
	if !val.Type().IsObjectType() {
		return Worker{}, fmt.Errorf("expected object, got %s", val.Type().FriendlyName())
	}

	w := Worker{Labels: map[string]string{}}
	if val.Type().HasAttribute("private_ip") {
		w.PrivateIP = val.GetAttr("private_ip").AsString()
	}
	if val.Type().HasAttribute("server_type") {
		w.ServerType = val.GetAttr("server_type").AsString()
	}
	if val.Type().HasAttribute("labels") {
		labels := val.GetAttr("labels")
		if !labels.IsNull() {
			it := labels.ElementIterator()
			for it.Next() {
				k, v := it.Element()
				w.Labels[k.AsString()] = v.AsString()
			}
		}
	}
	return w, nil
}

// writeFileAtomic writes data to path via a temp-file-then-rename, so a
// crash mid-write never leaves terraform.tfvars truncated.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp, path, err)
	}
	return nil
}
