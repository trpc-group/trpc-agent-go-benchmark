//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

const (
	lmeDigestAlgorithm = "sha256"
	lmeRedactedValue   = "[REDACTED]"
)

type lmeProvenanceDependencies struct {
	now           func() time.Time
	readBuildInfo func() (*debug.BuildInfo, bool)
	runGit        func(context.Context, ...string) ([]byte, error)
}

func defaultLMEProvenanceDependencies() lmeProvenanceDependencies {
	repoRoot, _ := findLMERepositoryRoot()
	var runGit func(context.Context, ...string) ([]byte, error)
	if repoRoot != "" {
		runGit = func(ctx context.Context, args ...string) ([]byte, error) {
			return runLMEProvenanceGitAtRoot(ctx, repoRoot, args...)
		}
	}
	return lmeProvenanceDependencies{
		now:           time.Now,
		readBuildInfo: debug.ReadBuildInfo,
		runGit:        runGit,
	}
}

func runLMEProvenanceGitAtRoot(
	ctx context.Context,
	repoRoot string,
	args ...string,
) ([]byte, error) {
	if !filepath.IsAbs(repoRoot) {
		return nil, fmt.Errorf("LongMemEval benchmark repository root is not absolute")
	}
	gitArgs := []string{
		"-c", "lfs.storage=/tmp/lfs-provenance",
		"-C", repoRoot,
	}
	gitArgs = append(gitArgs, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("run benchmark git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func findLMERepositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate LongMemEval working directory: %w", err)
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return "", fmt.Errorf("resolve LongMemEval working directory: %w", err)
	}
	for {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return current, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect LongMemEval repository marker: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("LongMemEval benchmark repository root is unavailable")
		}
		current = parent
	}
}

func collectLMECodeProvenance(
	ctx context.Context,
	deps lmeProvenanceDependencies,
) (lmeCodeProvenance, []string, map[string]string) {
	var buildInfo *debug.BuildInfo
	var buildInfoOK bool
	if deps.readBuildInfo != nil {
		buildInfo, buildInfoOK = deps.readBuildInfo()
	}
	code := lmeCodeProvenance{
		Benchmark: lmeBenchmarkProvenance{DirtyState: lmeDirtyStateUnknown},
	}
	unavailable := make(map[string]string)
	if buildInfoOK && buildInfo != nil {
		code.GoVersion = buildInfo.GoVersion
		code.Benchmark = benchmarkProvenanceFromBuildInfo(buildInfo)
		code.AgentModules = agentModulesFromBuildInfo(buildInfo, unavailable)
		code.RootRevision = rootLMEAgentRevision(code.AgentModules)
	} else {
		unavailable["code.go_build_info"] = "runtime build information is unavailable"
	}
	fillBenchmarkProvenanceFromGit(ctx, deps.runGit, &code.Benchmark, unavailable)

	var blockers []string
	if code.Benchmark.Revision == "" {
		blockers = append(blockers, "benchmark Git revision is unavailable")
		unavailable["code.benchmark.revision"] = "not present in build information or local Git"
	}
	switch code.Benchmark.DirtyState {
	case lmeDirtyStateDirty:
		blockers = append(blockers, "benchmark worktree is dirty")
	case lmeDirtyStateUnknown:
		blockers = append(blockers, "benchmark worktree state is unavailable")
		unavailable["code.benchmark.dirty_state"] = "not present in build information or local Git"
	}
	if len(code.AgentModules) == 0 {
		blockers = append(blockers, "trpc-agent-go module provenance is unavailable")
		unavailable["code.trpc_agent_go_modules"] = "no matching modules in Go build information"
	}
	if code.RootRevision == "" {
		blockers = append(blockers, "trpc-agent-go root module revision is unavailable")
		unavailable["code.trpc_agent_go_root_revision"] =
			"the resolved root module version does not contain a commit revision"
	}
	for _, module := range code.AgentModules {
		if module.LocalReplacement {
			blockers = append(blockers, fmt.Sprintf("module %s uses a local replacement", module.Path))
		}
		if !module.Resolved {
			blockers = append(blockers, fmt.Sprintf("module %s is unresolved", module.Path))
		}
		if module.Revision == "" {
			blockers = append(blockers, fmt.Sprintf("module %s revision is unavailable", module.Path))
		}
	}
	return code, uniqueSortedStrings(blockers), unavailable
}

func rootLMEAgentRevision(modules []lmeModuleProvenance) string {
	const rootModule = "trpc.group/trpc-go/trpc-agent-go"
	for _, module := range modules {
		if module.Path == rootModule {
			return module.Revision
		}
	}
	return ""
}

func benchmarkProvenanceFromBuildInfo(info *debug.BuildInfo) lmeBenchmarkProvenance {
	provenance := lmeBenchmarkProvenance{
		DirtyState: lmeDirtyStateUnknown,
		Source:     "go_build_info",
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			provenance.Revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			switch strings.TrimSpace(setting.Value) {
			case "true":
				provenance.DirtyState = lmeDirtyStateDirty
			case "false":
				provenance.DirtyState = lmeDirtyStateClean
			}
		}
	}
	if provenance.Revision == "" {
		provenance.Revision = revisionFromModuleVersion(info.Main.Version)
	}
	return provenance
}

func fillBenchmarkProvenanceFromGit(
	ctx context.Context,
	runGit func(context.Context, ...string) ([]byte, error),
	provenance *lmeBenchmarkProvenance,
	unavailable map[string]string,
) {
	if runGit == nil {
		return
	}
	gitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if provenance.Revision == "" {
		out, err := runGit(gitCtx, "rev-parse", "HEAD")
		if err == nil {
			provenance.Revision = strings.TrimSpace(string(out))
			provenance.Source = "git"
		} else {
			unavailable["code.benchmark.git_revision_error"] = "local Git revision lookup failed"
		}
	}
	if provenance.DirtyState != lmeDirtyStateUnknown {
		return
	}
	out, err := runGit(gitCtx, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		unavailable["code.benchmark.git_status_error"] = "local Git status lookup failed"
		return
	}
	if strings.TrimSpace(string(out)) == "" {
		provenance.DirtyState = lmeDirtyStateClean
	} else {
		provenance.DirtyState = lmeDirtyStateDirty
	}
	if provenance.Source == "" {
		provenance.Source = "git"
	}
}

func agentModulesFromBuildInfo(
	info *debug.BuildInfo,
	unavailable map[string]string,
) []lmeModuleProvenance {
	modules := make([]lmeModuleProvenance, 0)
	add := func(module *debug.Module) {
		if module == nil || !isTRPCAgentModule(module.Path) {
			return
		}
		provenance := lmeModuleProvenance{
			Path:             module.Path,
			RequestedVersion: module.Version,
			EffectivePath:    module.Path,
			EffectiveVersion: module.Version,
			Checksum:         module.Sum,
		}
		if module.Replace != nil {
			provenance.EffectivePath = module.Replace.Path
			provenance.EffectiveVersion = module.Replace.Version
			provenance.Checksum = module.Replace.Sum
			provenance.Replaced = true
			provenance.LocalReplacement = module.Replace.Version == ""
		}
		provenance.Revision = revisionFromModuleVersion(provenance.EffectiveVersion)
		provenance.Resolved = provenance.EffectiveVersion != "" &&
			provenance.EffectiveVersion != "(devel)" && !provenance.LocalReplacement
		if provenance.Revision == "" {
			unavailable["code.modules."+module.Path+".revision"] =
				"effective module version does not contain a commit revision"
		}
		modules = append(modules, provenance)
	}
	add(&info.Main)
	for _, module := range info.Deps {
		add(module)
	}
	sort.Slice(modules, func(i, j int) bool {
		return modules[i].Path < modules[j].Path
	})
	return modules
}

func isTRPCAgentModule(path string) bool {
	const modulePrefix = "trpc.group/trpc-go/trpc-agent-go"
	return path == modulePrefix || strings.HasPrefix(path, modulePrefix+"/")
}

func revisionFromModuleVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), "-")
	if len(parts) < 3 {
		return ""
	}
	candidate := parts[len(parts)-1]
	if len(candidate) < 7 {
		return ""
	}
	if _, err := hex.DecodeString(candidate); err != nil {
		return ""
	}
	return candidate
}

func collectLMEArtifact(path string, required bool) (lmeArtifactProvenance, error) {
	trimmed := strings.TrimSpace(path)
	artifact := lmeArtifactProvenance{
		Configured: trimmed != "",
		Path:       sanitizeLocation(trimmed),
	}
	if trimmed == "" {
		artifact.UnavailableReason = "not configured"
		if required {
			return artifact, fmt.Errorf("required artifact path is empty")
		}
		return artifact, nil
	}
	digest, err := digestLMEArtifact(trimmed)
	if err != nil {
		artifact.UnavailableReason = classifyArtifactError(err)
		if required {
			return artifact, fmt.Errorf("digest required artifact: %w", err)
		}
		return artifact, nil
	}
	artifact.Available = true
	artifact.Digest = lmeDigestAlgorithm + ":" + digest
	return artifact, nil
}

func collectLMEArtifactAt(
	path string,
	required bool,
	base string,
) (lmeArtifactProvenance, error) {
	artifact, err := collectLMEArtifact(path, required)
	if err != nil {
		return artifact, err
	}
	if !artifact.Configured {
		return artifact, nil
	}
	portable, err := portableLMEArtifactPath(base, path)
	if err != nil {
		artifact.Path = ""
		return artifact, nil
	}
	artifact.Path = portable
	return artifact, nil
}

func portableLMEArtifactPath(base, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve LongMemEval artifact path: %w", err)
	}
	if strings.TrimSpace(base) == "" {
		return filepath.ToSlash(filepath.Base(absPath)), nil
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve LongMemEval artifact base: %w", err)
	}
	relative, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", fmt.Errorf("make LongMemEval artifact path portable: %w", err)
	}
	if filepath.IsAbs(relative) || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("LongMemEval artifact is outside the shared result root")
	}
	return filepath.ToSlash(filepath.Clean(relative)), nil
}

func digestLMEArtifact(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symbolic-link artifacts are not supported")
	}
	if info.Mode().IsRegular() {
		return digestLMEFile(path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("unsupported artifact type %s", info.Mode().Type())
	}
	h := sha256.New()
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return fmt.Errorf("make artifact path relative: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact contains symbolic link %s", relative)
		}
		if entry.IsDir() {
			return writeLMEHashString(h, "D\x00"+relative+"\n")
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("artifact contains unsupported file %s", relative)
		}
		fileDigest, err := digestLMEFile(current)
		if err != nil {
			return fmt.Errorf("digest artifact file %s: %w", relative, err)
		}
		return writeLMEHashString(h, "F\x00"+relative+"\x00"+fileDigest+"\n")
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func digestLMEFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeLMEHashString(writer io.Writer, value string) error {
	if _, err := io.WriteString(writer, value); err != nil {
		return fmt.Errorf("hash artifact metadata: %w", err)
	}
	return nil
}

func classifyArtifactError(err error) string {
	if os.IsNotExist(err) {
		return "configured artifact does not exist"
	}
	if os.IsPermission(err) {
		return "configured artifact is not readable"
	}
	return "configured artifact could not be digested"
}

func sanitizeProvenanceMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = sanitizeProvenanceValue(key, value)
	}
	return out
}

func sanitizeProvenanceValue(key string, value any) any {
	if isSensitiveProvenanceKey(key) {
		return lmeRedactedValue
	}
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeProvenanceMap(typed)
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for nestedKey, nestedValue := range typed {
			converted[nestedKey] = nestedValue
		}
		return sanitizeProvenanceMap(converted)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeProvenanceValue("", item)
		}
		return out
	case string:
		if isEndpointProvenanceKey(key) {
			return sanitizeEndpoint(typed)
		}
		return typed
	default:
		return value
	}
}

func isSensitiveProvenanceKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	if strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "authorization") ||
		strings.Contains(normalized, "credential") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "passwd") ||
		strings.Contains(normalized, "secret") ||
		strings.HasSuffix(normalized, "dsn") {
		return true
	}
	switch normalized {
	case "token", "accesstoken", "refreshtoken", "bearertoken", "proxyauth":
		return true
	default:
		return false
	}
}

func isEndpointProvenanceKey(key string) bool {
	normalized := strings.ToLower(key)
	return strings.Contains(normalized, "url") ||
		strings.Contains(normalized, "host") ||
		strings.Contains(normalized, "endpoint") ||
		strings.Contains(normalized, "proxy")
}

func sanitizeLocation(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return sanitizeEndpoint(value)
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return filepath.Base(filepath.Clean(value))
	}
	return filepath.ToSlash(filepath.Clean(value))
}

func sanitizeEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return lmeRedactedValue
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func fingerprintLMEEndpoint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "provider-default"
	}
	return lmeDigestAlgorithm + ":" + lmeSHA256(sanitizeEndpoint(value))
}

func sanitizeProvenanceIdentifier(value string) string {
	value = sanitizeLMETraceString(strings.TrimSpace(value))
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return lmeRedactedValue
	}
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func rejectUnversionedLMEOutput(outputDir, manifestPath string) error {
	if _, err := os.Stat(manifestPath); err == nil {
		return fmt.Errorf("run_manifest.json already exists; use resume with the exact run configuration")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect run manifest: %w", err)
	}
	for _, name := range []string{"checkpoint.json", "results.json"} {
		path := filepath.Join(outputDir, name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s exists without run_manifest.json; use a fresh output directory", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect unversioned result %s: %w", name, err)
		}
	}
	return nil
}

func writeLMERunManifestCreateOnce(path string, manifest *lmeRunManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal LongMemEval run manifest: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".run_manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create run manifest temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set run manifest permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write run manifest temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync run manifest temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close run manifest temporary file: %w", err)
	}
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("run manifest already exists")
		}
		return fmt.Errorf("publish immutable run manifest: %w", err)
	}
	return syncLMEDirectory(dir)
}

func syncLMEDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open run manifest directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync run manifest directory: %w", err)
	}
	return nil
}

func readLMERunManifest(path string) (*lmeRunManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("read run manifest: artifact is unavailable")
	}
	var manifest lmeRunManifest
	if err := lmeDecodeStrict(data, &manifest); err != nil {
		return nil, errors.New("parse run manifest: invalid or non-canonical JSON")
	}
	if manifest.SchemaVersion != lmeRunManifestSchemaVersion {
		return nil, fmt.Errorf("unsupported run manifest schema %d", manifest.SchemaVersion)
	}
	digest, err := calculateLMERunCompatibilityDigest(&manifest)
	if err != nil {
		return nil, err
	}
	if digest != manifest.CompatibilityDigest {
		return nil, errors.New("run manifest compatibility digest is invalid")
	}
	comparisonDigest, err := calculateLMEComparisonDigest(&manifest)
	if err != nil {
		return nil, err
	}
	if comparisonDigest != manifest.ComparisonDigest {
		return nil, errors.New("run manifest comparison digest is invalid")
	}
	if err := validateLMERunManifestDeclaration(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func validateLMERunManifestDeclaration(manifest *lmeRunManifest) error {
	derived := deriveLMERunManifestBlockers(manifest)
	if !equalLMEStrings(manifest.OfficialBlockers, derived) {
		return errors.New("run manifest official blockers do not match provenance")
	}
	reproducible := len(derived) == 0
	status := lmeOfficialStatusBlocked
	if reproducible {
		status = lmeOfficialStatusEligible
	}
	if manifest.Reproducible != reproducible || manifest.OfficialStatus != status {
		return errors.New("run manifest official eligibility does not match provenance")
	}
	return nil
}

func bindLMERunManifest(result *lmeRunResult, manifest *lmeRunManifest) {
	if result == nil || manifest == nil {
		return
	}
	if result.Metadata == nil {
		result.Metadata = &lmeMetadata{}
	}
	result.Metadata.RunManifestVersion = manifest.SchemaVersion
	result.Metadata.RunCompatibility = manifest.CompatibilityDigest
	result.Metadata.RunComparison = manifest.ComparisonDigest
	result.Metadata.OfficialStatus = manifest.OfficialStatus
	result.Metadata.OfficialBlockers = append([]string(nil), manifest.OfficialBlockers...)
	result.Metadata.Config = sanitizeLMERunConfigForPersistence(result.Metadata.Config)
}

func verifyLMECheckpointManifest(result *lmeRunResult, manifest *lmeRunManifest) error {
	if result == nil || result.Metadata == nil {
		return errors.New("checkpoint has no run metadata")
	}
	if result.Metadata.RunManifestVersion != manifest.SchemaVersion {
		return fmt.Errorf(
			"checkpoint run manifest schema is %d, want %d",
			result.Metadata.RunManifestVersion,
			manifest.SchemaVersion,
		)
	}
	if result.Metadata.RunCompatibility != manifest.CompatibilityDigest {
		return errors.New("checkpoint does not belong to the immutable run manifest")
	}
	if result.Metadata.RunComparison != manifest.ComparisonDigest {
		return errors.New("checkpoint comparison digest does not match the immutable run manifest")
	}
	return nil
}

func sanitizeLMERunConfigForPersistence(cfg lmeRunConfig) lmeRunConfig {
	cfg.DatasetPath = sanitizeLocation(cfg.DatasetPath)
	cfg.ManifestPath = sanitizeLocation(cfg.ManifestPath)
	cfg.ReplayRoot = sanitizeLocation(cfg.ReplayRoot)
	cfg.BuildPlanRoot = sanitizeLocation(cfg.BuildPlanRoot)
	cfg.EmbeddingCachePath = sanitizeLocation(cfg.EmbeddingCachePath)
	cfg.Mem0Host = sanitizeEndpoint(cfg.Mem0Host)
	cfg.Mem0Version = sanitizeProvenanceIdentifier(cfg.Mem0Version)
	cfg.Mem0Revision = sanitizeProvenanceIdentifier(cfg.Mem0Revision)
	cfg.Mem0ProxyUsageLog = sanitizeLocation(cfg.Mem0ProxyUsageLog)
	cfg.Mem0ProxyRunID = sanitizeProvenanceIdentifier(cfg.Mem0ProxyRunID)
	return cfg
}

func equalLMEJSON(left, right any) bool {
	leftData, leftErr := canonicalLMEJSON(left)
	rightData, rightErr := canonicalLMEJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}

func canonicalLMEJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return json.Marshal(normalizeLMEProvenanceValue(decoded))
}
