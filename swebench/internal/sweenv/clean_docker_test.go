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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testImageID    = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	httpbinImageID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type cleanRoomDockerCommander struct {
	commands             []recordedCommand
	containerImages      map[string]string
	copiedSHA256         map[string]string
	inspectOverride      string
	checksumMismatchPath string
	failExec             bool
}

type imageResolutionCommander struct {
	commands   []recordedCommand
	identities map[string]string
}

func (c *imageResolutionCommander) Run(
	_ context.Context,
	env []string,
	name string,
	args ...string,
) ([]byte, error) {
	c.commands = append(c.commands, recordedCommand{
		env: append([]string(nil), env...), name: name, args: append([]string(nil), args...),
	})
	if len(args) == 5 && args[0] == "image" && args[1] == "inspect" {
		return []byte(c.identities[args[4]]), nil
	}
	return nil, errors.New("unexpected command")
}

func (c *cleanRoomDockerCommander) Run(
	_ context.Context,
	env []string,
	name string,
	args ...string,
) ([]byte, error) {
	c.commands = append(c.commands, recordedCommand{
		env: append([]string(nil), env...), name: name, args: append([]string(nil), args...),
	})
	if c.containerImages == nil {
		c.containerImages = map[string]string{}
	}
	if c.copiedSHA256 == nil {
		c.copiedSHA256 = map[string]string{}
	}
	if len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "run":
		container := argumentAfter(args, "--name")
		if len(args) >= 3 {
			c.containerImages[container] = args[len(args)-3]
		}
		return []byte(container), nil
	case "inspect":
		if c.inspectOverride != "" {
			return []byte(c.inspectOverride), nil
		}
		return []byte(c.containerImages[args[len(args)-1]]), nil
	case "exec":
		if c.failExec {
			return []byte("exec failed"), errors.New("exec failed")
		}
		if len(args) == 5 && args[2] == "sha256sum" && args[3] == "--" {
			containerPath := args[1] + ":" + args[4]
			digest, ok := c.copiedSHA256[containerPath]
			if !ok {
				return []byte("copied file not found"), errors.New("copied file not found")
			}
			if args[4] == c.checksumMismatchPath {
				digest = strings.Repeat("0", 64)
			}
			return []byte(digest + "  " + args[4] + "\n"), nil
		}
		return []byte("ok"), nil
	case "cp":
		if len(args) != 3 {
			return nil, errors.New("invalid docker cp")
		}
		digest, err := regularFileSHA256(args[1])
		if err != nil {
			return nil, err
		}
		c.copiedSHA256[args[2]] = digest
		return []byte("ok"), nil
	case "rm":
		return []byte("ok"), nil
	default:
		return []byte("ok"), nil
	}
}

func TestDockerFactoryCleanRoomStartsImmutableDisconnectedContainer(t *testing.T) {
	commander := &cleanRoomDockerCommander{}
	instanceID := "django__django-10000"
	reference := ImageForInstance(instanceID)
	factory := DockerFactory{
		Commander:      commander,
		CleanRoom:      true,
		CaseTimeout:    time.Hour,
		ResolvedImages: map[string]ImageIdentity{reference: {Reference: reference, ID: testImageID}},
	}
	environment, err := factory.StartCase(context.Background(), CaseSpec{
		InstanceID: instanceID,
		Repo:       "django/django",
		BaseCommit: strings.Repeat("1", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	de, ok := environment.(*dockerEnvironment)
	if !ok {
		t.Fatalf("environment type = %T", environment)
	}
	start := strings.Join(commander.commands[0].args, " ")
	for _, required := range []string{"--pull=never", "--network=none", "-w /testbed", testImageID + " sleep 3660"} {
		if !strings.Contains(start, required) {
			t.Fatalf("start command %q lacks %q", start, required)
		}
	}
	for _, forbidden := range []string{reference, "--cap-add", "--device", "--privileged"} {
		if strings.Contains(start, forbidden) {
			t.Fatalf("model start command %q contains %q", start, forbidden)
		}
	}
	if len(commander.commands) < 4 || commander.commands[2].args[len(commander.commands[2].args)-1] != "/testbed" {
		t.Fatalf("sanitize command does not pass explicit root: %#v", commander.commands)
	}
	if commander.commands[3].args[len(commander.commands[3].args)-1] != "/testbed" ||
		!strings.Contains(strings.Join(commander.commands[3].args, " "), "testbed HEAD changed after clean-room setup") {
		t.Fatalf("final Git verification command = %#v", commander.commands[3])
	}
	provenance := de.Provenance()
	if provenance.Testbed.Reference != reference || provenance.Testbed.ID != testImageID {
		t.Fatalf("provenance = %#v", provenance)
	}

	result := environment.Execute(context.Background(), "env")
	if result.ReturnCode != 0 {
		t.Fatalf("Execute() = %#v", result)
	}
	execute := strings.Join(commander.commands[len(commander.commands)-1].args, " ")
	for _, variable := range []string{"PIP_NO_INDEX=1", "UV_OFFLINE=1", "npm_config_offline=true"} {
		if !strings.Contains(execute, variable) {
			t.Fatalf("execute command %q lacks %q", execute, variable)
		}
	}
}

func TestDockerFactoryStartCaseKeepsNonCleanDefaultBehavior(t *testing.T) {
	commander := &fakeCommander{}
	instanceID := "repo__repo-1"
	factory := DockerFactory{
		Commander:             commander,
		EnableOfflineServices: true,
		CaseTimeout:           time.Hour,
	}
	if _, err := factory.StartCase(context.Background(), CaseSpec{
		InstanceID: instanceID,
		BaseCommit: "not-used-outside-clean-room",
	}); err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 1 {
		t.Fatalf("commands = %#v", commander.commands)
	}
	start := strings.Join(commander.commands[0].args, " ")
	if !strings.Contains(start, ImageForInstance(instanceID)) {
		t.Fatalf("non-clean start %q lacks mutable default image reference", start)
	}
	for _, cleanOnly := range []string{"--pull=never", "--network=none", "--add-host", "--cap-add", "--device"} {
		if strings.Contains(start, cleanOnly) {
			t.Fatalf("non-clean start %q contains clean-room option %q", start, cleanOnly)
		}
	}
}

func TestDockerFactoryCleanRoomRequiresCaseIdentity(t *testing.T) {
	factory := DockerFactory{CleanRoom: true, Commander: &cleanRoomDockerCommander{}}
	if _, err := factory.Start(context.Background(), "repo__repo-1"); err == nil ||
		!strings.Contains(err.Error(), "requires StartCase") {
		t.Fatalf("Start error = %v", err)
	}
	if _, err := factory.StartCase(context.Background(), CaseSpec{InstanceID: "repo__repo-1"}); err == nil ||
		!strings.Contains(err.Error(), "repo") {
		t.Fatalf("StartCase error = %v", err)
	}
	if _, err := factory.StartCase(context.Background(), CaseSpec{
		InstanceID: "repo__repo-1", Repo: "repo/repo",
	}); err == nil || !strings.Contains(err.Error(), "base commit") {
		t.Fatalf("StartCase base error = %v", err)
	}
	reference := ImageForInstance("repo__repo-1")
	factory.ResolvedImages = map[string]ImageIdentity{
		reference: {Reference: "different", ID: testImageID},
	}
	if _, err := factory.StartCase(context.Background(), CaseSpec{
		InstanceID: "repo__repo-1", Repo: "repo/repo", BaseCommit: strings.Repeat("1", 40),
	}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("invalid resolved image error = %v", err)
	}
}

func TestDockerFactoryCleanRoomSetupFailureCleansContainer(t *testing.T) {
	commander := &cleanRoomDockerCommander{inspectOverride: httpbinImageID}
	instanceID := "repo__repo-1"
	reference := ImageForInstance(instanceID)
	factory := DockerFactory{
		CleanRoom: true,
		Commander: commander,
		ResolvedImages: map[string]ImageIdentity{
			reference: {Reference: reference, ID: testImageID},
		},
	}
	_, err := factory.StartCase(context.Background(), CaseSpec{
		InstanceID: instanceID, Repo: "repo/repo", BaseCommit: strings.Repeat("1", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "uses image") {
		t.Fatalf("StartCase error = %v", err)
	}
	if len(commander.commands) != 3 || strings.Join(commander.commands[2].args, " ") !=
		"rm -f "+argumentAfter(commander.commands[0].args, "--name") {
		t.Fatalf("cleanup commands = %#v", commander.commands)
	}
}

func TestDockerFactoryCleanRoomOfflineServicesLeastPrivilegeAndCleanupOrder(t *testing.T) {
	commander := &cleanRoomDockerCommander{}
	instanceID := "psf__requests-2317"
	reference := ImageForInstance(instanceID)
	assets := writeAssetTestBundle(t, withOfflineHTTPBinTestAssets(map[string]assetTestFile{
		offlineSourceImages: {content: "source images\n"},
		offlineTarpitBinary: {content: "binary", mode: 0o755},
	}))
	assetIdentity, err := InspectOfflineAssets(assets, []string{instanceID})
	if err != nil {
		t.Fatal(err)
	}
	certs := writeOfflineHTTPBinTestCerts(t)
	factory := DockerFactory{
		CleanRoom:             true,
		EnableOfflineServices: true,
		OfflineAssetsDir:      assets,
		OfflineAssets:         assetIdentity,
		HTTPBinCerts:          &certs,
		Commander:             commander,
		CaseTimeout:           time.Hour,
		ResolvedImages: map[string]ImageIdentity{
			reference:           {Reference: reference, ID: testImageID},
			offlineHTTPBinImage: {Reference: offlineHTTPBinImage, ID: httpbinImageID},
		},
	}
	environment, err := factory.StartCase(context.Background(), CaseSpec{
		InstanceID: instanceID,
		Repo:       "psf/requests",
		BaseCommit: strings.Repeat("2", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	de := environment.(*dockerEnvironment)
	if len(de.sidecars) != 2 {
		t.Fatalf("sidecars = %q", de.sidecars)
	}
	modelRun, helperRun, httpbinRun := dockerRunCommands(t, commander.commands)
	model := strings.Join(modelRun.args, " ")
	for _, required := range []string{"--network=none", "--add-host httpbin.org:127.0.0.1", testImageID} {
		if !strings.Contains(model, required) {
			t.Fatalf("model run %q lacks %q", model, required)
		}
	}
	for _, forbidden := range []string{"--cap-add", "--device", "--privileged"} {
		if strings.Contains(model, forbidden) {
			t.Fatalf("model run %q contains %q", model, forbidden)
		}
	}
	helper := strings.Join(helperRun.args, " ")
	for _, required := range []string{
		"--network=container:" + de.name,
		"--cap-drop=ALL", "--cap-add=NET_ADMIN", "--device=/dev/net/tun:/dev/net/tun",
		"--security-opt no-new-privileges:true", testImageID,
	} {
		if !strings.Contains(helper, required) {
			t.Fatalf("helper run %q lacks %q", helper, required)
		}
	}
	httpbin := strings.Join(httpbinRun.args, " ")
	for _, required := range []string{
		"--network=container:" + de.name,
		"--cap-drop=ALL", "--cap-add=NET_BIND_SERVICE",
		"--security-opt no-new-privileges:true", httpbinImageID,
	} {
		if !strings.Contains(httpbin, required) {
			t.Fatalf("httpbin run %q lacks %q", httpbin, required)
		}
	}
	for _, forbidden := range []string{"NET_ADMIN", "--device", "--privileged"} {
		if strings.Contains(httpbin, forbidden) {
			t.Fatalf("httpbin run %q contains %q", httpbin, forbidden)
		}
	}
	provenance := de.Provenance()
	if provenance.AuxiliaryImages["network-helper"].ID != testImageID ||
		provenance.AuxiliaryImages["httpbin"].ID != httpbinImageID {
		t.Fatalf("auxiliary image provenance = %#v", provenance.AuxiliaryImages)
	}
	copyCount := 0
	for index, command := range commander.commands {
		if len(command.args) == 0 || command.args[0] != "cp" {
			continue
		}
		copyCount++
		if index+1 >= len(commander.commands) {
			t.Fatalf("docker cp has no following checksum command: %#v", command)
		}
		container, path, ok := strings.Cut(command.args[2], ":")
		if !ok {
			t.Fatalf("docker cp destination = %q", command.args[2])
		}
		want := []string{"exec", container, "sha256sum", "--", path}
		next := commander.commands[index+1].args
		if strings.Join(next, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("command after docker cp = %#v, want %#v", next, want)
		}
	}
	if copyCount != 4 {
		t.Fatalf("verified docker copies = %d, want 4", copyCount)
	}

	commandCount := len(commander.commands)
	if err := environment.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	removals := commander.commands[commandCount:]
	if len(removals) != 3 {
		t.Fatalf("remove commands = %#v", removals)
	}
	wantOrder := []string{de.sidecars[1], de.sidecars[0], de.name}
	for index, want := range wantOrder {
		if got := strings.Join(removals[index].args, " "); got != "rm -f "+want {
			t.Fatalf("remove[%d] = %q, want %q", index, got, "rm -f "+want)
		}
	}
}

func TestDockerFactoryCopiedOfflineAssetChecksumMismatchFailsClosed(t *testing.T) {
	tests := []struct {
		name              string
		instanceID        string
		files             map[string]assetTestFile
		mismatchPath      string
		resolvedHTTPBin   bool
		forbiddenCommands []string
	}{
		{
			name:       "dependency blocks pip install",
			instanceID: "psf__requests-2931",
			files: withOfflineHTTPBinTestAssets(map[string]assetTestFile{
				offlineSourceImages:                 {content: "source images\n"},
				offlineTarpitBinary:                 {content: "binary", mode: 0o755},
				"requests-2931/requirements.txt":    {content: "requests==2.4.3\n"},
				"requests-2931/wheels/requests.whl": {content: "wheel"},
			}),
			mismatchPath:      offlineWheelhouseRoot + "/requests-2931/requirements.txt",
			forbiddenCommands: []string{"pip install", "chmod 0555", "gunicorn"},
		},
		{
			name:       "tarpit blocks chmod and launch",
			instanceID: "psf__requests-2317",
			files: withOfflineHTTPBinTestAssets(map[string]assetTestFile{
				offlineSourceImages: {content: "source images\n"},
				offlineTarpitBinary: {content: "binary", mode: 0o755},
			}),
			mismatchPath:      offlineTarpitContainerPath,
			forbiddenCommands: []string{"chmod 0555", "gunicorn"},
		},
		{
			name:       "server certificate blocks service launch",
			instanceID: "psf__requests-9999",
			files: withOfflineHTTPBinTestAssets(map[string]assetTestFile{
				offlineSourceImages: {content: "source images\n"},
			}),
			mismatchPath:      offlineHTTPBinCertPath,
			resolvedHTTPBin:   true,
			forbiddenCommands: []string{"gunicorn"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assets := writeAssetTestBundle(t, test.files)
			assetIdentity, err := InspectOfflineAssets(assets, []string{test.instanceID})
			if err != nil {
				t.Fatal(err)
			}
			commander := &cleanRoomDockerCommander{checksumMismatchPath: test.mismatchPath}
			reference := ImageForInstance(test.instanceID)
			resolved := map[string]ImageIdentity{
				reference: {Reference: reference, ID: testImageID},
			}
			if test.resolvedHTTPBin {
				resolved[offlineHTTPBinImage] = ImageIdentity{
					Reference: offlineHTTPBinImage,
					ID:        httpbinImageID,
				}
			}
			factory := DockerFactory{
				CleanRoom:             true,
				EnableOfflineServices: true,
				OfflineAssetsDir:      assets,
				OfflineAssets:         assetIdentity,
				Commander:             commander,
				CaseTimeout:           time.Hour,
				ResolvedImages:        resolved,
			}
			_, err = factory.StartCase(context.Background(), CaseSpec{
				InstanceID: test.instanceID,
				Repo:       "psf/requests",
				BaseCommit: strings.Repeat("2", 40),
			})
			if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
				t.Fatalf("StartCase error = %v", err)
			}
			for _, forbidden := range test.forbiddenCommands {
				if dockerCommandContains(commander.commands, forbidden) {
					t.Fatalf("commands unexpectedly contain %q: %#v", forbidden, commander.commands)
				}
			}
			if !dockerCommandContains(commander.commands, "rm -f") {
				t.Fatalf("setup failure did not clean containers: %#v", commander.commands)
			}
		})
	}
}

func TestImageSetSHA256StableAndRejectsAliases(t *testing.T) {
	first := map[string]ImageIdentity{
		"z": {Reference: "z", ID: testImageID},
		"a": {Reference: "a", ID: httpbinImageID},
	}
	second := map[string]ImageIdentity{
		"a": {Reference: "a", ID: httpbinImageID},
		"z": {Reference: "z", ID: testImageID},
	}
	firstHash, err := ImageSetSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := ImageSetSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash || len(firstHash) != 64 {
		t.Fatalf("hashes = %q and %q", firstHash, secondHash)
	}
	if _, err := ImageSetSHA256(map[string]ImageIdentity{
		"a": {Reference: "alias", ID: testImageID},
	}); err == nil {
		t.Fatal("aliased image identity unexpectedly accepted")
	}
	if hash, err := ImageSetSHA256(nil); err != nil || hash != "" {
		t.Fatalf("empty hash = %q, %v", hash, err)
	}
}

func TestDockerFactoryResolveImagesBeforeCasesAndDeduplicatesReferences(t *testing.T) {
	djangoReference := ImageForInstance("django__django-10000")
	requestsReference := ImageForInstance("psf__requests-2317")
	commander := &imageResolutionCommander{identities: map[string]string{
		djangoReference:     testImageID,
		offlineHTTPBinImage: httpbinImageID,
		requestsReference:   "sha256:" + strings.Repeat("c", 64),
	}}
	factory := DockerFactory{
		CleanRoom: true, EnableOfflineServices: true, Commander: commander,
	}
	images, err := factory.ResolveImages(context.Background(), []CaseSpec{
		{InstanceID: "psf__requests-2317", Repo: "psf/requests", BaseCommit: strings.Repeat("1", 40)},
		{InstanceID: "django__django-10000", Repo: "django/django", BaseCommit: strings.Repeat("2", 40)},
		{InstanceID: "psf__requests-2317", Repo: "psf/requests", BaseCommit: strings.Repeat("1", 40)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 3 || len(commander.commands) != 3 {
		t.Fatalf("images = %#v, commands = %#v", images, commander.commands)
	}
	previous := ""
	for _, command := range commander.commands {
		reference := command.args[len(command.args)-1]
		if previous != "" && reference < previous {
			t.Fatalf("image references not resolved in order: %q before %q", previous, reference)
		}
		previous = reference
		if got := images[reference]; got.Reference != reference || got.ID != commander.identities[reference] {
			t.Fatalf("identity for %s = %#v", reference, got)
		}
	}
}

func TestDockerFactoryResolveImagesRejectsInvalidInputsAndInspectOutput(t *testing.T) {
	commander := &imageResolutionCommander{identities: map[string]string{}}
	factory := DockerFactory{CleanRoom: true, Commander: commander}
	if _, err := factory.ResolveImages(context.Background(), []CaseSpec{{InstanceID: "repo__repo-1"}}); err == nil ||
		!strings.Contains(err.Error(), "repo") {
		t.Fatalf("invalid case error = %v", err)
	}
	if len(commander.commands) != 0 {
		t.Fatalf("commands before identity validation = %#v", commander.commands)
	}
	reference := ImageForInstance("repo__repo-1")
	commander.identities[reference] = "not-an-image-id"
	if _, err := factory.ResolveImages(context.Background(), []CaseSpec{{
		InstanceID: "repo__repo-1", Repo: "repo/repo", BaseCommit: strings.Repeat("1", 40),
	}}); err == nil || !strings.Contains(err.Error(), "invalid image ID") {
		t.Fatalf("invalid inspect output error = %v", err)
	}

	nonCleanCommander := &imageResolutionCommander{}
	images, err := (DockerFactory{Commander: nonCleanCommander}).ResolveImages(context.Background(), []CaseSpec{{}})
	if err != nil || images != nil || len(nonCleanCommander.commands) != 0 {
		t.Fatalf("non-clean resolution = %#v, %v, commands %#v", images, err, nonCleanCommander.commands)
	}
}

func writeOfflineHTTPBinTestCerts(t *testing.T) OfflineHTTPBinCerts {
	t.Helper()
	directory := t.TempDir()
	write := func(name string) string {
		file := filepath.Join(directory, name)
		if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}
	return OfflineHTTPBinCerts{
		CABundle: write("ca.pem"), ServerCert: write("server.crt"), ServerKey: write("server.key"),
	}
}

func dockerRunCommands(t *testing.T, commands []recordedCommand) (recordedCommand, recordedCommand, recordedCommand) {
	t.Helper()
	var runs []recordedCommand
	for _, command := range commands {
		if len(command.args) > 0 && command.args[0] == "run" {
			runs = append(runs, command)
		}
	}
	if len(runs) != 3 {
		t.Fatalf("Docker run commands = %#v", runs)
	}
	return runs[0], runs[1], runs[2]
}

func argumentAfter(args []string, argument string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == argument {
			return args[index+1]
		}
	}
	return ""
}

func dockerCommandContains(commands []recordedCommand, fragment string) bool {
	for _, command := range commands {
		if strings.Contains(strings.Join(command.args, " "), fragment) {
			return true
		}
	}
	return false
}
