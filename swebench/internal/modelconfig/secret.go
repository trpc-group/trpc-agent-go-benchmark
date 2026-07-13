//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package modelconfig

import "strings"

// RedactSecrets returns a copy with common secret values redacted.
func RedactSecrets(cfg EnvConfig) EnvConfig {
	out := EnvConfig{}
	for k, v := range cfg {
		if IsSecretKey(k) {
			out[k] = "REDACTED"
			continue
		}
		out[k] = v
	}
	return out
}

// IsSecretKey reports whether a config key likely contains credentials.
func IsSecretKey(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "key") ||
		strings.Contains(k, "token") ||
		strings.Contains(k, "secret") ||
		strings.Contains(k, "authorization")
}
