//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package benchruntime contains small shared helpers for memory benchmarks.
package benchruntime

import "strings"

// TableNameWithSuffix appends suffix to base when suffix is not empty.
func TableNameWithSuffix(base string, suffix string) string {
	if suffix == "" {
		return base
	}
	return base + suffix
}

// SanitizeCacheFileName returns a filesystem-safe cache name component.
func SanitizeCacheFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}
