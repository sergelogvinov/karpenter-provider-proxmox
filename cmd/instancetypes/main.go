/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package main implements the Karpenter Proxmox Instance Types generator.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	cobra "github.com/spf13/cobra"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/instancetype"
)

var (
	command = "instancetypes"
	version = "v0.0.0"
	commit  = "none"
)

func main() {
	if exitCode := run(); exitCode != 0 {
		os.Exit(exitCode)
	}
}

func run() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := cobra.Command{
		Use:     command,
		Version: fmt.Sprintf("%s (commit: %s)", version, commit),
		Short:   "A command-line utility to generate instance types for Karpenter Proxmox",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Flags()

			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(buildGenerateCmd())

	err := cmd.ExecuteContext(ctx)
	if err != nil {
		errorString := err.Error()
		if strings.Contains(errorString, "arg(s)") || strings.Contains(errorString, "flag") || strings.Contains(errorString, "command") {
			fmt.Fprintf(os.Stderr, "Error: %s\n\n", errorString)
			fmt.Fprintln(os.Stderr, cmd.UsageString())
		} else {
			fmt.Fprintln(os.Stderr, "Execute error:", err)
		}

		return 1
	}

	return 0
}

type generateCmd struct {
	options instancetype.InstanceTypeOptions
}

func buildGenerateCmd() *cobra.Command {
	c := &generateCmd{}

	cmd := cobra.Command{
		Use:           "generate instance types",
		Aliases:       []string{"g"},
		Short:         "Generate instance types for Karpenter Proxmox",
		Args:          cobra.ExactArgs(0),
		PreRunE:       c.parseArgs,
		RunE:          c.runGenerate,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := cmd.Flags()
	flags.StringP("cpus", "c", "1,2,4", "comma-separated list of vCPU counts")
	flags.StringP("memfactor", "m", "2,3,4", "comma-separated list of memory multipliers per vCPU")
	flags.IntP("storage", "s", 30, "storage size in GiB, omitted if zero")

	flags.BoolP("kubelet", "", false, "add resources allocated to kubernetes kubelet")
	flags.BoolP("system", "", false, "add resources allocated to the OS system daemons")
	flags.BoolP("eviction", "", false, "add default eviction threshold")

	return &cmd
}

func (c *generateCmd) parseArgs(cmd *cobra.Command, args []string) (err error) {
	flags := cmd.Flags()

	c.options.CPUs = []int{}
	if cpusStr, err := flags.GetString("cpus"); err == nil {
		for s := range strings.SplitSeq(cpusStr, ",") {
			if i, err := strconv.Atoi(s); err == nil {
				c.options.CPUs = append(c.options.CPUs, i)
			}
		}
	}

	c.options.MemFactors = []int{}
	if memFactorsStr, err := flags.GetString("memfactor"); err == nil {
		for s := range strings.SplitSeq(memFactorsStr, ",") {
			if i, err := strconv.Atoi(s); err == nil {
				c.options.MemFactors = append(c.options.MemFactors, i)
			}
		}
	}

	c.options.Storage = 0
	if storageStr, err := flags.GetInt("storage"); err == nil {
		c.options.Storage = storageStr
	}

	c.options.KubeletOverhead, err = flags.GetBool("kubelet")
	if err != nil {
		return err
	}

	c.options.SystemOverhead, err = flags.GetBool("system")
	if err != nil {
		return err
	}

	c.options.EvictionThreshold, err = flags.GetBool("eviction")
	if err != nil {
		return err
	}

	return nil
}

func (c *generateCmd) runGenerate(cmd *cobra.Command, args []string) error {
	instanceTypes := c.options.Generate()

	jsonData, err := json.MarshalIndent(instanceTypes, "", "  ")
	if err != nil {
		return err
	}

	fmt.Println(string(jsonData))

	return nil
}
