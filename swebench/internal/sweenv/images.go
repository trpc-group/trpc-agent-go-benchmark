//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

const imageSetSchema = "swebench-docker-image-set-v1"

// ResolveImages resolves every selected clean-room image before resume is
// evaluated. Containers subsequently start by immutable ID rather than by the
// mutable requested tag.
func (f DockerFactory) ResolveImages(
	ctx context.Context,
	specs []CaseSpec,
) (map[string]ImageIdentity, error) {
	if !f.CleanRoom {
		return nil, nil
	}
	references := map[string]struct{}{}
	for _, spec := range specs {
		if err := validateCleanRoomCaseSpec(spec); err != nil {
			return nil, err
		}
		references[ImageForInstance(spec.InstanceID)] = struct{}{}
		if f.EnableOfflineServices && usesOfflineHTTPBin(spec.InstanceID) {
			references[offlineHTTPBinImage] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(references))
	for reference := range references {
		ordered = append(ordered, reference)
	}
	sort.Strings(ordered)
	resolved := make(map[string]ImageIdentity, len(ordered))
	for _, reference := range ordered {
		identity, err := f.resolveDockerImage(ctx, reference)
		if err != nil {
			return nil, err
		}
		resolved[reference] = identity
	}
	return resolved, nil
}

// ImageSetSHA256 returns a stable identity for a resolved reference-to-image-ID
// map. It rejects missing, malformed, or aliased entries.
func ImageSetSHA256(images map[string]ImageIdentity) (string, error) {
	if len(images) == 0 {
		return "", nil
	}
	references := make([]string, 0, len(images))
	for reference, identity := range images {
		if reference == "" || identity.Reference != reference || !dockerImageID.MatchString(identity.ID) {
			return "", fmt.Errorf("invalid Docker image-set entry for %q", reference)
		}
		references = append(references, reference)
	}
	sort.Strings(references)
	h := sha256.New()
	_, _ = fmt.Fprintln(h, imageSetSchema)
	for _, reference := range references {
		_, _ = fmt.Fprintf(h, "%s\x00%s\n", reference, images[reference].ID)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
