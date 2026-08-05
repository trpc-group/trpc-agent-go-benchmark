//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package contract

// Prediction is the SWE-Bench prediction shape consumed by the official harness.
type Prediction struct {
	ModelNameOrPath string `json:"model_name_or_path"`
	InstanceID      string `json:"instance_id"`
	ModelPatch      string `json:"model_patch"`
}
