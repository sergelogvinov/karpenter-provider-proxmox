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

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"

	local "github.com/sergelogvinov/go-proxmox-local"
	"github.com/sergelogvinov/go-proxmox-local/qemu"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/proxmox/zonecapacity/topology"
	utilsysinfo "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/systeminfo"

	"k8s.io/utils/cpuset"

	"sigs.k8s.io/karpenter/pkg/utils/env"
)

const (
	verbosityEnvVarName = "VERBOSITY"
	verbosityFlagName   = "verbosity"

	watchPathEnvVarName = "WATCH_PATH"
	watchPathFlagName   = "watch-path"

	maxRetriesEnvVarName = "MAX_RETRIES"
	maxRetriesFlagName   = "max-retries"

	resyncIntervalEnvVarName = "RESYNC_INTERVAL"
	resyncIntervalFlagName   = "resync-interval"

	cpuGovernorBusyEnvVarName = "CPU_GOVERNOR_BUSY"
	cpuGovernorBusyFlagName   = "cpu-governor-busy"

	cpuGovernorFreeEnvVarName = "CPU_GOVERNOR_FREE"
	cpuGovernorFreeFlagName   = "cpu-governor-free"

	sharedPolicyEnvVarName = "SHARED_POLICY"
	sharedPolicyFlagName   = "shared-policy"

	reservedCPUsEnvVarName = "RESERVED_CPUS"
	reservedCPUsFlagName   = "reserved-cpus"

	sharedMinWidthEnvVarName = "SHARED_MIN_WIDTH"
	sharedMinWidthFlagName   = "shared-min-width"

	rebalanceDebounceEnvVarName = "REBALANCE_DEBOUNCE"
	rebalanceDebounceFlagName   = "rebalance-debounce"
)

var (
	// Version of the proxmox-scheduler
	Version = "edge"

	showVersion = pflag.Bool("version", false, "Print the version and exit.")

	verbosity      = pflag.IntP(verbosityFlagName, "v", env.WithDefaultInt(verbosityEnvVarName, 0), "Verbosity level (0=info, 1=debug, 2=trace, -1=errors only)")
	watchPath      = pflag.String(watchPathFlagName, env.WithDefaultString(watchPathEnvVarName, "/run/qemu-server"), "Path to watch of qemu pid files")
	maxRetries     = pflag.Int(maxRetriesFlagName, env.WithDefaultInt(maxRetriesEnvVarName, 5), "Maximum number of retry attempts")
	resyncInterval = pflag.Duration(resyncIntervalFlagName, env.WithDefaultDuration(resyncIntervalEnvVarName, 60*time.Minute), "Resync interval")

	cpuGovernorBusy = pflag.String(cpuGovernorBusyFlagName, env.WithDefaultString(cpuGovernorBusyEnvVarName, "performance"), "CPU governor to set when CPU is busy")
	cpuGovernorFree = pflag.String(cpuGovernorFreeFlagName, env.WithDefaultString(cpuGovernorFreeEnvVarName, "powersave"), "CPU governor to set when CPU is free")

	sharedPolicy      = pflag.String(sharedPolicyFlagName, env.WithDefaultString(sharedPolicyEnvVarName, SharedPolicyPartition), "Placement policy for VMs without affinity: partition, none")
	reservedCPUs      = pflag.String(reservedCPUsFlagName, env.WithDefaultString(reservedCPUsEnvVarName, ""), "Host-only CPUs excluded from every pool, e.g. 0,32")
	sharedMinWidth    = pflag.Int(sharedMinWidthFlagName, env.WithDefaultInt(sharedMinWidthEnvVarName, 0), "Minimum CPUs per shared VM in oversubscribed mode (0 = one physical core)")
	rebalanceDebounce = pflag.Duration(rebalanceDebounceFlagName, env.WithDefaultDuration(rebalanceDebounceEnvVarName, 2*time.Second), "Event coalescing window before a shared CPU pool rebalance")
)

func main() {
	pflag.Parse()

	logger := setupLogger(*verbosity)
	logger.Info("Proxmox VM scheduler", "version", Version, "verbosity", *verbosity)

	if *showVersion {
		os.Exit(0)
	}

	featureFlagsStr := os.Getenv("PROXMOX_FEATURE_FLAGS")
	featureFlags := parseFeatureFlags(featureFlagsStr)
	logger.Info("Feature flags configured", "featureFlags", featureFlags)
	logger.Info("Scheduler configured",
		"cpuGovernorBusy", *cpuGovernorBusy,
		"cpuGovernorFree", *cpuGovernorFree,
		"sharedPolicy", *sharedPolicy,
		"reservedCPUs", *reservedCPUs,
	)

	logger.Info("Collecting server hardware information...")

	serverInfo, err := utilsysinfo.CollectServerInfo()
	if err != nil {
		logger.Error(err, "Failed to collect server information")
		os.Exit(1)
	}

	var tp *topology.Topology

	tp, err = topology.DiscoverCadvisor(logger, serverInfo)
	if err != nil {
		logger.Error(err, "Failed to discover CPU topology")
		os.Exit(1)
	}

	showServerInfo(logger, serverInfo, tp)

	client, err := local.New(local.WithRunDir(*watchPath))
	if err != nil {
		logger.Error(err, "Failed to create Proxmox local client")
		os.Exit(1)
	}

	if featureFlags.IsEnabled(FeatureKarpenter) {
		if tp != nil && serverInfo != nil {
			if err := ensureNodeCapacityGuest(logger, client, serverInfo, tp); err != nil {
				logger.Error(err, "Failed to create or update the node-capacity guest")
			}
		}
	}

	reservedCPUSet := cpuset.New()

	if *reservedCPUs != "" {
		reservedCPUSet, err = cpuset.Parse(*reservedCPUs)
		if err != nil {
			logger.Error(err, "Failed to parse reserved CPUs", "reservedCPUs", *reservedCPUs)
			os.Exit(1)
		}
	}

	handler, err := NewHandler(client, tp, logger,
		WithReservedCPUs(reservedCPUSet),
		WithSharedPolicy(*sharedPolicy),
		WithSharedMinWidth(*sharedMinWidth),
		WithRebalanceDebounce(*rebalanceDebounce),
	)
	if err != nil {
		logger.Error(err, "Failed to set up the CPU scheduler")
		os.Exit(1)
	}

	if err := scheduler(client, handler, logger); err != nil {
		logger.Error(err, "Scheduler encountered an error")
		os.Exit(1)
	}
}

func scheduler(client *local.Client, handler *SchedulerHandler, logger logr.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("Watching virtual machines", "path", *watchPath)

	if err := watchVMs(ctx, client, handler); err != nil {
		logger.Error(err, "Failed to watch virtual machines")

		return err
	}

	logger.Info("Received signal, shut down gracefully")

	return nil
}

// watchVMs tells handler of every VM start, VM stop and sync of the VMs on this
// node until ctx is canceled, retrying and syncing as --max-retries and
// --resync-interval say. It returns nil once ctx is canceled.
func watchVMs(ctx context.Context, client *local.Client, handler qemu.EventHandler) error {
	return client.Qemu().Watch(ctx, handler, qemu.WatchOptions{
		SyncInterval: *resyncInterval,
		MaxRetries:   *maxRetries,
	})
}
