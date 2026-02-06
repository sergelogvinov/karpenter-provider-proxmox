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

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"
	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/reconciler"
	utilsysinfo "github.com/sergelogvinov/karpenter-provider-proxmox/pkg/utils/systeminfo"

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
)

var (
	// Version of the proxmox-scheduler
	Version = "edge"

	showVersion = pflag.Bool("version", false, "Print the version and exit.")

	verbosity      = pflag.IntP(verbosityFlagName, "v", env.WithDefaultInt(verbosityEnvVarName, 0), "Verbosity level (0=info, 1=debug, 2=trace, -1=errors only)")
	watchPath      = pflag.String(watchPathFlagName, env.WithDefaultString(watchPathEnvVarName, "/run/qemu-server"), "Path to watch of qemu pid files")
	maxRetries     = pflag.Int(maxRetriesFlagName, env.WithDefaultInt(maxRetriesEnvVarName, 5), "Maximum number of retry attempts")
	resyncInterval = pflag.Duration(resyncIntervalFlagName, env.WithDefaultDuration(resyncIntervalEnvVarName, 60*time.Minute), "Resync interval")
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

	logger.Info("Collecting server hardware information...")

	serverInfo, err := utilsysinfo.CollectServerInfo()
	if err != nil {
		logger.Error(err, "Failed to collect server information")
		os.Exit(1)
	}

	var tp *topology.CPUTopology

	tp, err = topology.DiscoverCadvisor(logger, serverInfo)
	if err != nil {
		logger.Error(err, "Failed to discover CPU topology")
		os.Exit(1)
	}

	showServerInfo(logger, serverInfo, tp)

	if featureFlags.IsEnabled(FeatureKarpenter) {
		if tp != nil && serverInfo != nil {
			if err := createProxmoxTopologyDiscoveryVM(logger, serverInfo, tp); err != nil {
				logger.Error(err, "Failed to create Proxmox VM")
			}
		}
	}

	if err := scheduler(NewHandler(tp, logger), logger); err != nil {
		logger.Error(err, "Reconciler encountered an error")
		os.Exit(1)
	}
}

func scheduler(handler *SchedulerHandler, logger logr.Logger) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	config := reconciler.DefaultConfig(logger)
	config.MaxRetries = *maxRetries
	config.WatchPath = *watchPath
	config.SyncDelay = *resyncInterval

	rec, err := reconciler.NewReconciler(ctx, cancel, config, handler)
	if err != nil {
		logger.Error(err, "Failed to create reconciler")

		return err
	}

	if err := rec.Start(); err != nil {
		logger.Error(err, "Failed to start reconciler")

		return err
	}

	logger.Info("Reconciler started successfully")

	select {
	case sig := <-sigCh:
		logger.Info("Received signal, shutting down gracefully", "signal", sig)
	case <-ctx.Done():
		logger.Info("Context canceled, shutting down")
	}

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		defer close(done)

		rec.Stop()
	}()

	select {
	case <-done:
		logger.Info("Reconciler stopped gracefully")
	case <-shutdownCtx.Done():
		logger.Info("Shutdown timeout exceeded, forcing exit")
	}

	return nil
}
