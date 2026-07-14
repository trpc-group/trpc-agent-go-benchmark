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
	targetHelp     = "baseline or mini-go"
)

func validateTarget(target string) error {
	switch target {
	case targetBaseline, targetMiniGo:
		return nil
	default:
		return fmt.Errorf("-target must be baseline or mini-go")
	}
}
