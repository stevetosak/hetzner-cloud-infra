// Package cmd is the kluster command line. Every command that changes
// anything runs in Plan Mode unless given --apply (ADR 0004).
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

var flags struct {
	config          string
	env             string
	apply           bool
	allowUnexpected bool
}

var rootCmd = &cobra.Command{
	Use:   "kluster",
	Short: "Build the Hetzner Kubernetes cluster from its runbooks, safely",
	Long: "kluster carries out the runbooks in docs/runbook as code. It plans before it acts, " +
		"checks every Terraform plan against what the command intends, and changes nothing " +
		"without --apply (ADR 0004, ADR 0009).",
	SilenceUsage: true,
}

// ExecuteContext runs the CLI.
func ExecuteContext(ctx context.Context) error { return rootCmd.ExecuteContext(ctx) }

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&flags.config, "config", "kluster.yaml", "path to kluster.yaml")
	pf.StringVar(&flags.env, "env", config.EnvLive, "where to act: live or rehearsal")
	pf.BoolVar(&flags.apply, "apply", false, "act; without it every command only plans")
	pf.BoolVar(&flags.allowUnexpected, "allow-unexpected", false,
		"apply although a plan does not match its intent; prints every mismatch it overrides")
}

// app is what one command run needs. It is built by run and torn down when
// the command returns, failed runs included.
type app struct {
	cfg     *config.Config
	env     *env.Environment
	api     *cloud.Client
	run     *tf.RunDir
	pins    *hostkey.Pins
	mode    tf.Mode
	out     io.Writer
	modules map[string]*tf.Module
}

// run builds the app and calls fn. A rehearsal run first proves it is not
// pointed at the live project.
func run(cmd *cobra.Command, fn func(ctx context.Context, a *app) error) error {
	ctx := cmd.Context()
	cfg, err := config.Load(flags.config)
	if err != nil {
		return err
	}
	e, err := env.Resolve(cfg, flags.env, os.Getenv)
	if err != nil {
		return err
	}
	pinsPath, err := hostkey.DefaultPinsPath(e.Name)
	if err != nil {
		return err
	}
	pins, err := hostkey.OpenPins(pinsPath)
	if err != nil {
		return err
	}
	rd, err := tf.NewRunDir()
	if err != nil {
		return err
	}
	defer rd.Close()

	a := &app{
		cfg:     cfg,
		env:     e,
		api:     cloud.New(e.HcloudToken()),
		run:     rd,
		pins:    pins,
		mode:    tf.Mode{Apply: flags.apply, AllowUnexpected: flags.allowUnexpected},
		out:     cmd.OutOrStdout(),
		modules: map[string]*tf.Module{},
	}
	fmt.Fprintf(a.out, "kluster: env %s, %s\n", e.Name, modeName(a.mode))
	if !e.IsLive() {
		if err := a.api.GuardNotProject(ctx, e.RefuseProjectWithIP); err != nil {
			return err
		}
	}
	return fn(ctx, a)
}

// module opens a Terraform Module once per run.
func (a *app) module(ctx context.Context, name string) (*tf.Module, error) {
	if m, ok := a.modules[name]; ok {
		return m, nil
	}
	dir, err := a.cfg.ModuleDir(name)
	if err != nil {
		return nil, err
	}
	m, err := tf.Open(ctx, name, dir, a.env, a.run)
	if err != nil {
		return nil, err
	}
	a.modules[name] = m
	return m, nil
}

// requireRehearsal refuses a command that exists only for the rehearsal
// project.
func (a *app) requireRehearsal(what string) error {
	if a.env.IsLive() {
		return errors.New(what + " runs only with --env rehearsal; it is refused for the live project")
	}
	return nil
}

func modeName(m tf.Mode) string {
	if m.Apply {
		return "--apply"
	}
	return "Plan Mode (nothing changes; --apply to act)"
}
