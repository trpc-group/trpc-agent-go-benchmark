//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package native

import (
	"context"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
)

const (
	nativeRuntimeTRPC   = "trpc"
	nativeRuntimeLegacy = "legacy"
)

func nativeRuntime(runtime string) string {
	runtime = strings.TrimSpace(runtime)
	if runtime == "" {
		return nativeRuntimeTRPC
	}
	return runtime
}

func runInstanceWithRuntime(ctx context.Context, client *chatClient, inst dataset.Instance, ws workspace, req RunRequest) loopResult {
	if nativeRuntime(req.AgentRuntime) == nativeRuntimeLegacy {
		return runInstance(ctx, client, inst, ws, req)
	}
	return runInstanceWithTRPCAgent(ctx, inst, ws, req)
}
