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
	"github.com/go-logr/logr"
	info "github.com/google/cadvisor/info/v1"
	"go.uber.org/zap/zapcore"

	"github.com/sergelogvinov/karpenter-provider-proxmox/pkg/providers/cloudcapacity/cpumanager/topology"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func setupLogger(verbosity int) logr.Logger {
	opt := &zap.Options{
		Development:     true,
		Level:           zapcore.Level(-verbosity),
		StacktraceLevel: zapcore.PanicLevel,
		EncoderConfigOptions: []zap.EncoderConfigOption{
			func(ec *zapcore.EncoderConfig) {
				ec.TimeKey = ""  // Disable timestamp
				ec.LevelKey = "" // Disable log level
			},
		},
	}

	return zap.New(zap.UseFlagOptions(opt))
}

func showServerInfo(logger logr.Logger, serverInfo *info.MachineInfo, tp *topology.CPUTopology) {
	logger.Info("===== Server Hardware Information =====")
	defer logger.Info("=======================================")

	if tp == nil || serverInfo == nil {
		logger.Info("No server information available")

		return
	}

	logger.Info("CPU Information", "topology", tp)

	if len(tp.CPUDetails) > 0 {
		logger.Info("NUMA Topology Information")

		for nodeID := range tp.NumNUMANodes {
			logger.Info("NUMA Node",
				"nodeID", nodeID,
				"sockets", tp.CPUDetails.SocketsInNUMANodes(nodeID),
				"cpus", tp.CPUDetails.CPUsInNUMANodes(nodeID),
				"memory", serverInfo.Topology[nodeID].Memory,
			)
		}
	}
}
