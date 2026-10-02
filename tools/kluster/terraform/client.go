package terraform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// Client wraps the terraform CLI for the operations kluster needs:
// applying the worker set, reading outputs, and tainting a resource for
// forced recreation.
type Client struct {
	tf *tfexec.Terraform
}

// NewClient creates a Client rooted at workDir (the infra/ directory
// containing main.tf, terraform.tfvars, etc.), using the terraform binary
// found on PATH.
func NewClient(workDir string) (*Client, error) {
	tf, err := tfexec.NewTerraform(workDir, "terraform")
	if err != nil {
		return nil, fmt.Errorf("creating terraform client for %s: %w", workDir, err)
	}
	return &Client{tf: tf}, nil
}

// Apply runs `terraform apply -auto-approve` with the given -var
// assignments, mirroring the flags reset-nodes.sh already passes today
// (allow_public_ssh, node_suffix).
func (c *Client) Apply(ctx context.Context, vars map[string]string) error {
	opts := make([]tfexec.ApplyOption, 0, len(vars))
	for k, v := range vars {
		opts = append(opts, tfexec.Var(fmt.Sprintf("%s=%s", k, v)))
	}
	if err := c.tf.Apply(ctx, opts...); err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}
	return nil
}

// WorkerPublicIPs returns the `worker_public_ips` output, in the same
// key-sorted order Terraform's `for` expression over the workers map
// produces (matching WorkerNames element-for-element).
func (c *Client) WorkerPublicIPs(ctx context.Context) ([]string, error) {
	return c.stringListOutput(ctx, "worker_public_ips")
}

// WorkerNames returns the `worker_names` output — the live server names
// (tfvars key + "-" + node_suffix), not the stable tfvars keys.
func (c *Client) WorkerNames(ctx context.Context) ([]string, error) {
	return c.stringListOutput(ctx, "worker_names")
}

func (c *Client) stringListOutput(ctx context.Context, name string) ([]string, error) {
	outputs, err := c.tf.Output(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading terraform output: %w", err)
	}
	raw, ok := outputs[name]
	if !ok {
		return nil, fmt.Errorf("output %q not found", name)
	}
	var result []string
	if err := json.Unmarshal(raw.Value, &result); err != nil {
		return nil, fmt.Errorf("decoding output %q: %w", name, err)
	}
	return result, nil
}

// Taint marks a resource address (e.g. `hcloud_server.workers["k8swk1"]`)
// for recreation on the next apply.
func (c *Client) Taint(ctx context.Context, address string) error {
	if err := c.tf.Taint(ctx, address); err != nil {
		return fmt.Errorf("terraform taint %s: %w", address, err)
	}
	return nil
}
