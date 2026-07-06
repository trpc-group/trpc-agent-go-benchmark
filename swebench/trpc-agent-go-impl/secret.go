//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

func isSecretKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(k, "api_key") ||
		strings.Contains(k, "authorization") ||
		strings.Contains(k, "token") ||
		strings.Contains(k, "secret") ||
		strings.Contains(k, "provider")
}

func redactJSONBytes(data []byte) []byte {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return redactTextBytes(data)
	}
	literals := collectSecretLiterals(v)
	v = redactJSONValue(v, literals)
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return redactTextBytes(data)
	}
	return append(out, '\n')
}

func collectSecretLiterals(v any) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(any)
	walk = func(cur any) {
		switch x := cur.(type) {
		case map[string]any:
			for k, val := range x {
				if isSecretKey(k) {
					if s, ok := val.(string); ok && shouldCollectSecretLiteral(s) && !seen[s] {
						seen[s] = true
						out = append(out, s)
					}
				}
				walk(val)
			}
		case []any:
			for _, elem := range x {
				walk(elem)
			}
		}
	}
	walk(v)
	return out
}

func shouldCollectSecretLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 4 || s == "<redacted>" {
		return false
	}
	return true
}

func redactJSONValue(v any, literals []string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			key := redactLiterals(redactTextString(k), literals)
			if isSecretKey(k) {
				out[key] = "<redacted>"
				continue
			}
			if s, ok := val.(string); ok {
				out[key] = redactLiterals(redactTextString(s), literals)
				continue
			}
			out[key] = redactJSONValue(val, literals)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, elem := range x {
			if s, ok := elem.(string); ok {
				out[i] = redactLiterals(redactTextString(s), literals)
				continue
			}
			out[i] = redactJSONValue(elem, literals)
		}
		return out
	}
	return v
}

var textSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key["'\s:=]+)([^"',\s\\}]+)`),
	regexp.MustCompile(`(?i)(authorization["'\s:=]+bearer\s+)([^"',\s\\}]+)`),
	regexp.MustCompile(`(?i)(x-smg-provider["'\s:=]+)([^"',\s\\}]+)`),
	regexp.MustCompile(`sk-[A-Za-z0-9._-]+`),
}

func redactTextBytes(data []byte) []byte {
	return []byte(redactTextString(string(data)))
}

func redactTextString(s string) string {
	out := s
	for _, re := range textSecretPatterns[:3] {
		out = re.ReplaceAllString(out, `${1}<redacted>`)
	}
	out = textSecretPatterns[3].ReplaceAllString(out, "<redacted>")
	return out
}

func redactLiterals(s string, literals []string) string {
	out := s
	for _, lit := range literals {
		out = strings.ReplaceAll(out, lit, "<redacted>")
	}
	return out
}
