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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	imagePrefix                = "docker.io/swebench/sweb.eval.x86_64."
	defaultContainerNamePrefix = "swebench-"
	maxContainerNameLength     = 63
)

var (
	unsafeContainerName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)
	dockerImageID       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Commander makes Docker lifecycle behavior testable without a daemon.
type Commander interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

type osCommander struct{}

func (osCommander) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// DockerFactory creates official SWE-Bench per-instance containers.
type DockerFactory struct {
	Config                Config
	DockerHost            string
	CommandTimeout        time.Duration
	CaseTimeout           time.Duration
	ContainerNamePrefix   string
	Commander             Commander
	Labels                map[string]string
	CleanRoom             bool
	GitIgnoredAllowlist   []string
	EnableOfflineServices bool
	OfflineAssetsDir      string
	OfflineAssets         OfflineAssetIdentity
	HTTPBinCerts          *OfflineHTTPBinCerts
	ResolvedImages        map[string]ImageIdentity
}

// ImageForInstance returns the official SWE-Bench image name.
func ImageForInstance(instanceID string) string {
	name := strings.ToLower(strings.ReplaceAll(instanceID, "__", "_1776_"))
	return imagePrefix + name + ":latest"
}

// Start launches a sleeping testbed container rooted at /testbed. Clean-room
// mode requires StartCase so the expected base commit cannot be omitted.
func (f DockerFactory) Start(ctx context.Context, instanceID string) (Environment, error) {
	if f.CleanRoom {
		return nil, fmt.Errorf("clean-room Docker factory requires StartCase")
	}
	return f.start(ctx, CaseSpec{InstanceID: instanceID})
}

// StartCase launches one case-aware testbed. With CleanRoom disabled it keeps
// the same Docker behavior as Start.
func (f DockerFactory) StartCase(ctx context.Context, spec CaseSpec) (Environment, error) {
	return f.start(ctx, spec)
}

func (f DockerFactory) start(ctx context.Context, spec CaseSpec) (Environment, error) {
	instanceID := spec.InstanceID
	commander := f.Commander
	if commander == nil {
		commander = osCommander{}
	}
	imageReference := ImageForInstance(instanceID)
	image := imageReference
	provenance := Provenance{}
	if f.CleanRoom {
		if err := validateCleanRoomCaseSpec(spec); err != nil {
			return nil, err
		}
		identity, err := f.resolveDockerImage(ctx, imageReference)
		if err != nil {
			return nil, err
		}
		image = identity.ID
		provenance.Testbed = identity
	}
	caseTimeout := f.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	prefix := f.ContainerNamePrefix
	if prefix == "" {
		prefix = defaultContainerNamePrefix
	}
	name := unsafeContainerName.ReplaceAllString(prefix+instanceID, "-")
	suffix := "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	maxBase := maxContainerNameLength - len(suffix)
	if len(name) > maxBase {
		name = name[:maxBase]
	}
	name += suffix
	args := []string{"run", "-d", "--rm"}
	if f.CleanRoom {
		args = append(args, "--pull=never", "--network=none")
	}
	args = append(args, "--name", name)
	useHTTPBin := f.CleanRoom && f.EnableOfflineServices && usesOfflineHTTPBin(instanceID)
	if useHTTPBin {
		args = append(args, "--add-host", offlineHTTPBinHost+":127.0.0.1")
	}
	labelKeys := make([]string, 0, len(f.Labels))
	for key := range f.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		args = append(args, "--label", key+"="+f.Labels[key])
	}
	args = append(args, "-w", "/testbed", image, "sleep", strconv.Itoa(int(caseTimeout.Seconds())+60))
	out, err := commander.Run(ctx, dockerEnv(f.DockerHost), "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("start Docker testbed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	environment := &dockerEnvironment{
		name:           name,
		config:         f.Config,
		dockerHost:     f.DockerHost,
		commandTimeout: f.CommandTimeout,
		commander:      commander,
		provenance:     provenance,
	}
	if f.CleanRoom {
		environment.setExtraEnv(map[string]string{
			"PIP_NO_INDEX":       "1",
			"UV_OFFLINE":         "1",
			"npm_config_offline": "true",
		})
		if err := f.verifyContainerImage(ctx, environment.name, provenance.Testbed.ID); err != nil {
			f.closeAfterSetupFailure(environment)
			return nil, err
		}
		if err := f.sanitizeGitRepository(ctx, environment, spec.BaseCommit); err != nil {
			f.closeAfterSetupFailure(environment)
			return nil, err
		}
	}
	if useHTTPBin {
		entries, err := f.prepareOfflineRequestAssets(ctx, environment, instanceID)
		if err != nil {
			f.closeAfterSetupFailure(environment)
			return nil, err
		}
		if err := f.startOfflineHTTPBin(ctx, environment, entries); err != nil {
			f.closeAfterSetupFailure(environment)
			return nil, err
		}
	}
	if f.CleanRoom {
		if err := f.verifyGitRepository(ctx, environment, spec.BaseCommit); err != nil {
			f.closeAfterSetupFailure(environment)
			return nil, err
		}
	}
	return environment, nil
}

func validateCleanRoomCaseSpec(spec CaseSpec) error {
	if spec.InstanceID == "" || strings.TrimSpace(spec.InstanceID) != spec.InstanceID {
		return fmt.Errorf("clean-room case instance ID %q is missing or invalid", spec.InstanceID)
	}
	if spec.Repo == "" || strings.TrimSpace(spec.Repo) != spec.Repo ||
		!strings.HasPrefix(spec.InstanceID, strings.ReplaceAll(spec.Repo, "/", "__")+"-") {
		return fmt.Errorf("clean-room repo %q does not match instance %q", spec.Repo, spec.InstanceID)
	}
	if !fullGitCommit.MatchString(spec.BaseCommit) {
		return fmt.Errorf("clean-room base commit for %s is missing or invalid", spec.InstanceID)
	}
	return nil
}

func (f DockerFactory) closeAfterSetupFailure(environment *dockerEnvironment) {
	closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = environment.Close(closeCtx)
}

type dockerEnvironment struct {
	name           string
	config         Config
	dockerHost     string
	commandTimeout time.Duration
	commander      Commander
	sidecars       []string
	extraEnv       map[string]string
	provenance     Provenance
}

func (e *dockerEnvironment) Execute(ctx context.Context, command string) CommandResult {
	timeout := e.commandTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"exec", "-w", "/testbed"}
	environment := make(map[string]string, len(e.config.Environment.Env)+len(e.extraEnv))
	for key, value := range e.config.Environment.Env {
		environment[key] = value
	}
	for key, value := range e.extraEnv {
		environment[key] = value
	}
	envKeys := make([]string, 0, len(environment))
	for key := range environment {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		args = append(args, "-e", key+"="+environment[key])
	}
	args = append(args, e.name)
	args = append(args, e.config.Environment.Interpreter...)
	args = append(args, command)
	out, err := e.commander.Run(commandCtx, dockerEnv(e.dockerHost), "docker", args...)
	result := CommandResult{Output: strings.ToValidUTF8(string(out), "�")}
	if err == nil {
		return result
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		result.ReturnCode = -1
		result.ExceptionInfo = fmt.Sprintf("An error occurred while executing the command: command timed out after %s", timeout)
		result.TimedOut = true
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ReturnCode = exitErr.ExitCode()
		return result
	}
	result.ReturnCode = 1
	result.ExceptionInfo = err.Error()
	return result
}

func (e *dockerEnvironment) Close(ctx context.Context) error {
	containers := make([]string, 0, len(e.sidecars)+1)
	for index := len(e.sidecars) - 1; index >= 0; index-- {
		containers = append(containers, e.sidecars[index])
	}
	containers = append(containers, e.name)
	var closeErrs []error
	for _, name := range containers {
		out, err := e.commander.Run(ctx, dockerEnv(e.dockerHost), "docker", "rm", "-f", name)
		if err != nil {
			closeErrs = append(closeErrs, fmt.Errorf(
				"remove Docker container %s: %w: %s",
				name,
				err,
				strings.TrimSpace(string(out)),
			))
		}
	}
	return errors.Join(closeErrs...)
}

// Provenance returns a defensive copy of the immutable image identities used
// by this environment.
func (e *dockerEnvironment) Provenance() Provenance {
	result := e.provenance
	if len(e.provenance.AuxiliaryImages) > 0 {
		result.AuxiliaryImages = make(map[string]ImageIdentity, len(e.provenance.AuxiliaryImages))
		for role, identity := range e.provenance.AuxiliaryImages {
			result.AuxiliaryImages[role] = identity
		}
	}
	return result
}

func (e *dockerEnvironment) setExtraEnv(values map[string]string) {
	if e.extraEnv == nil {
		e.extraEnv = make(map[string]string, len(values))
	}
	for key, value := range values {
		e.extraEnv[key] = value
	}
}

func (e *dockerEnvironment) setAuxiliaryImage(role string, identity ImageIdentity) {
	if e.provenance.AuxiliaryImages == nil {
		e.provenance.AuxiliaryImages = map[string]ImageIdentity{}
	}
	e.provenance.AuxiliaryImages[role] = identity
}

func (f DockerFactory) inspectDockerImage(ctx context.Context, reference string) (string, error) {
	commander := f.Commander
	if commander == nil {
		commander = osCommander{}
	}
	out, err := commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"image", "inspect", "--format", "{{.Id}}", reference,
	)
	if err != nil {
		return "", fmt.Errorf("inspect Docker image %s: %w: %s", reference, err, strings.TrimSpace(string(out)))
	}
	imageID := strings.TrimSpace(string(out))
	if !dockerImageID.MatchString(imageID) {
		return "", fmt.Errorf("inspect Docker image %s returned invalid image ID %q", reference, imageID)
	}
	return imageID, nil
}

func (f DockerFactory) resolveDockerImage(ctx context.Context, reference string) (ImageIdentity, error) {
	if identity, ok := f.ResolvedImages[reference]; ok {
		if identity.Reference != reference || !dockerImageID.MatchString(identity.ID) {
			return ImageIdentity{}, fmt.Errorf("resolved Docker image identity for %s is invalid", reference)
		}
		return identity, nil
	}
	imageID, err := f.inspectDockerImage(ctx, reference)
	if err != nil {
		return ImageIdentity{}, err
	}
	return ImageIdentity{Reference: reference, ID: imageID}, nil
}

func (f DockerFactory) verifyContainerImage(ctx context.Context, name, expectedID string) error {
	commander := f.Commander
	if commander == nil {
		commander = osCommander{}
	}
	out, err := commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"inspect", "--format", "{{.Image}}", name,
	)
	if err != nil {
		return fmt.Errorf("inspect Docker container %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	actual := strings.TrimSpace(string(out))
	if actual != expectedID {
		return fmt.Errorf("Docker container %s uses image %q, want %q", name, actual, expectedID)
	}
	return nil
}

func dockerEnv(host string) []string {
	if strings.TrimSpace(host) == "" {
		return nil
	}
	return []string{"DOCKER_HOST=" + host}
}
