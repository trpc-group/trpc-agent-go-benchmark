//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import "fmt"

const (
	targetBaseline = "baseline"
	targetMiniGo   = "mini-go"
	targetTAG      = "tag"
	targetHelp     = "baseline, mini-go, or tag"
)

func validateTarget(target string) error {
	switch target {
	case targetBaseline, targetMiniGo, targetTAG:
		return nil
	default:
		return fmt.Errorf("-target must be baseline, mini-go, or tag")
	}
}
