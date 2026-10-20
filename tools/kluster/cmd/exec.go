package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
)

func init() {
	rootCmd.AddCommand(execCmd)
}

var execCmd = &cobra.Command{
	Use:   "exec [--env <env>] -- <command> [args...]",
	Short: "Run a command with an environment's credentials, in place of sourcing infra/.envrc",
	Long: "Runs the command with the environment's Hetzner token as TF_VAR_HCLOUD_TOKEN and its R2 " +
		"keys as AWS_*, read from its Bitwarden item. Every TF_*, AWS_*, HCLOUD_* and BW_* variable of the " +
		"shell is dropped first. Outside live, TF_DATA_DIR and the init backend bucket point at that " +
		"environment, so `terraform -chdir=<module>` never touches live state. The cluster token is " +
		"never handed on. kluster is replaced by the command, so its exit code is the command's own; " +
		"there is no --apply, because the command has its own.",
	Example: "  kluster exec --env rehearsal -- terraform -chdir=../../infra/shared plan",
	Args:    cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := checkExecArgs(cmd.ArgsLenAtDash(), flags.apply || flags.allowUnexpected); err != nil {
			return err
		}
		ctx := cmd.Context()
		cfg, err := config.Load(flags.config)
		if err != nil {
			return err
		}
		e, err := env.Resolve(ctx, cfg, flags.env, os.Getenv, bitwarden.New(cmd.ErrOrStderr()))
		if err != nil {
			return err
		}
		if !e.IsLive() {
			if err := cloud.New(e.HcloudToken()).GuardNotProject(ctx, e.RefuseProjectWithIP); err != nil {
				return err
			}
		}
		path, envv, err := execTarget(e, args[0], os.Environ())
		if err != nil {
			return err
		}
		// Standard error, so the command's own output stays clean for a pipe.
		fmt.Fprintf(cmd.ErrOrStderr(), "kluster: env %s, exec %s\n", e.Name, args[0])
		return syscall.Exec(path, args, envv)
	},
}

// checkExecArgs refuses a call whose command is not after `--`, so the
// command's own flags never reach kluster, and the kluster flags that act.
func checkExecArgs(argsLenAtDash int, acts bool) error {
	if argsLenAtDash != 0 {
		return errors.New("put the command after --: kluster exec [--env <env>] -- <command> [args...]")
	}
	if acts {
		return errors.New("kluster exec takes no --apply or --allow-unexpected: the command it runs has its own")
	}
	return nil
}

// execTarget finds the command and builds its environment, in a stable order.
func execTarget(e *env.Environment, name string, base []string) (string, []string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", nil, err
	}
	m := e.ExecEnv(base)
	envv := make([]string, 0, len(m))
	for k, v := range m {
		envv = append(envv, k+"="+v)
	}
	sort.Strings(envv)
	return path, envv, nil
}
