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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	offlineHTTPBinHost       = "httpbin.org"
	offlineHTTPBinImage      = "docker.io/kennethreitz/httpbin:latest"
	offlineHTTPBinCACertPath = "/tmp/swebench-httpbin-ca.pem"
	offlineHTTPBinCertPath   = "/tmp/swebench-httpbin-server.crt"
	offlineHTTPBinKeyPath    = "/tmp/swebench-httpbin-server.key"
	offlineHTTPBinCAAsset    = "httpbin/ca.pem"
	offlineHTTPBinCertAsset  = "httpbin/server.crt"
	offlineHTTPBinKeyAsset   = "httpbin/server.key"
)

// OfflineHTTPBinCerts contains the local CA and server certificate copied into
// one isolated requests testbed and its loopback-only fixture sidecar.
type OfflineHTTPBinCerts struct {
	CABundle   string
	ServerCert string
	ServerKey  string
}

func usesOfflineHTTPBin(instanceID string) bool {
	return strings.HasPrefix(instanceID, "psf__requests-")
}

func (f DockerFactory) startOfflineHTTPBin(
	ctx context.Context,
	environment *dockerEnvironment,
	entries []offlineAssetEntry,
) error {
	certs := f.HTTPBinCerts
	if certs == nil {
		bundled, err := f.offlineHTTPBinCerts(entries)
		if err != nil {
			return err
		}
		certs = &bundled
	}
	if err := validateOfflineHTTPBinCerts(*certs); err != nil {
		return err
	}
	caSHA256, err := f.offlineHTTPBinCertSHA256(
		certs.CABundle,
		offlineHTTPBinCAAsset,
		entries,
		f.HTTPBinCerts == nil,
	)
	if err != nil {
		return err
	}
	if err := f.dockerCopy(ctx, certs.CABundle, environment.name+":"+offlineHTTPBinCACertPath); err != nil {
		return fmt.Errorf("copy offline httpbin CA into testbed: %w", err)
	}
	if err := f.verifyContainerFileSHA256(
		ctx,
		environment.name,
		offlineHTTPBinCACertPath,
		caSHA256,
	); err != nil {
		return fmt.Errorf("verify copied offline httpbin CA in testbed: %w", err)
	}

	identity, err := f.resolveDockerImage(ctx, offlineHTTPBinImage)
	if err != nil {
		return err
	}
	sidecar := offlineSidecarName(environment.name, "-httpbin")
	args := []string{
		"run", "-d", "--rm", "--pull=never",
		"--network=container:" + environment.name,
		"--cap-drop=ALL", "--cap-add=NET_BIND_SERVICE",
		"--security-opt", "no-new-privileges:true",
		"--name", sidecar,
	}
	args = appendDockerLabels(args, f.Labels)
	caseTimeout := f.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	args = append(args, identity.ID, "sleep", strconv.Itoa(int(caseTimeout.Seconds())+60))
	out, err := environment.commander.Run(ctx, dockerEnv(f.DockerHost), "docker", args...)
	if err != nil {
		return fmt.Errorf("start offline httpbin sidecar: %w: %s", err, strings.TrimSpace(string(out)))
	}
	environment.sidecars = append(environment.sidecars, sidecar)
	if err := f.verifyContainerImage(ctx, sidecar, identity.ID); err != nil {
		return err
	}
	environment.setAuxiliaryImage("httpbin", identity)

	serverCertSHA256, err := f.offlineHTTPBinCertSHA256(
		certs.ServerCert,
		offlineHTTPBinCertAsset,
		entries,
		f.HTTPBinCerts == nil,
	)
	if err != nil {
		return err
	}
	if err := f.dockerCopy(ctx, certs.ServerCert, sidecar+":"+offlineHTTPBinCertPath); err != nil {
		return fmt.Errorf("copy offline httpbin server certificate: %w", err)
	}
	if err := f.verifyContainerFileSHA256(
		ctx,
		sidecar,
		offlineHTTPBinCertPath,
		serverCertSHA256,
	); err != nil {
		return fmt.Errorf("verify copied offline httpbin server certificate: %w", err)
	}
	serverKeySHA256, err := f.offlineHTTPBinCertSHA256(
		certs.ServerKey,
		offlineHTTPBinKeyAsset,
		entries,
		f.HTTPBinCerts == nil,
	)
	if err != nil {
		return err
	}
	if err := f.dockerCopy(ctx, certs.ServerKey, sidecar+":"+offlineHTTPBinKeyPath); err != nil {
		return fmt.Errorf("copy offline httpbin server key: %w", err)
	}
	if err := f.verifyContainerFileSHA256(
		ctx,
		sidecar,
		offlineHTTPBinKeyPath,
		serverKeySHA256,
	); err != nil {
		return fmt.Errorf("verify copied offline httpbin server key: %w", err)
	}
	serviceCommand := "gunicorn -b 127.0.0.1:80 httpbin:app -k gevent " +
		">/tmp/swebench-httpbin-http.log 2>&1 & " +
		"exec gunicorn -b 127.0.0.1:443 httpbin:app -k gevent " +
		"--certfile " + offlineHTTPBinCertPath + " --keyfile " + offlineHTTPBinKeyPath + " " +
		">/tmp/swebench-httpbin-https.log 2>&1"
	out, err = environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", "-d", sidecar, "sh", "-c", serviceCommand,
	)
	if err != nil {
		return fmt.Errorf("launch offline httpbin services: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := waitOfflineHTTPBin(ctx, environment); err != nil {
		return err
	}
	environment.setExtraEnv(map[string]string{
		"CURL_CA_BUNDLE":     offlineHTTPBinCACertPath,
		"HTTPBIN_URL":        "http://" + offlineHTTPBinHost,
		"NO_PROXY":           "localhost,127.0.0.1,httpbin.org,10.255.255.1",
		"REQUESTS_CA_BUNDLE": offlineHTTPBinCACertPath,
		"SSL_CERT_FILE":      offlineHTTPBinCACertPath,
		"no_proxy":           "localhost,127.0.0.1,httpbin.org,10.255.255.1",
	})
	return nil
}

func (f DockerFactory) offlineHTTPBinCerts(entries []offlineAssetEntry) (OfflineHTTPBinCerts, error) {
	certs := OfflineHTTPBinCerts{
		CABundle:   filepath.Join(f.OfflineAssetsDir, filepath.FromSlash(offlineHTTPBinCAAsset)),
		ServerCert: filepath.Join(f.OfflineAssetsDir, filepath.FromSlash(offlineHTTPBinCertAsset)),
		ServerKey:  filepath.Join(f.OfflineAssetsDir, filepath.FromSlash(offlineHTTPBinKeyAsset)),
	}
	for label, relative := range map[string]string{
		"CA bundle":          offlineHTTPBinCAAsset,
		"server certificate": offlineHTTPBinCertAsset,
		"server key":         offlineHTTPBinKeyAsset,
	} {
		entry, ok := findOfflineAssetEntry(entries, relative)
		if !ok {
			return certs, fmt.Errorf("offline httpbin asset bundle has no %s", relative)
		}
		actual, err := regularFileSHA256(filepath.Join(f.OfflineAssetsDir, filepath.FromSlash(relative)))
		if err != nil {
			return certs, fmt.Errorf("offline httpbin %s: %w", label, err)
		}
		if actual != entry.sha256 {
			return certs, fmt.Errorf("offline httpbin asset changed before copy: %s", relative)
		}
	}
	return certs, nil
}

func (f DockerFactory) offlineHTTPBinCertSHA256(
	source string,
	relative string,
	entries []offlineAssetEntry,
	requireManifestEntry bool,
) (string, error) {
	actual, err := regularFileSHA256(source)
	if err != nil {
		return "", fmt.Errorf("offline httpbin asset %s: %w", relative, err)
	}
	if !requireManifestEntry {
		return actual, nil
	}
	entry, ok := findOfflineAssetEntry(entries, relative)
	if !ok {
		return "", fmt.Errorf("offline httpbin asset bundle has no %s", relative)
	}
	if actual != entry.sha256 {
		return "", fmt.Errorf("offline httpbin asset changed before copy: %s", relative)
	}
	return entry.sha256, nil
}

func (f DockerFactory) startOfflineTarpitHelper(
	ctx context.Context,
	environment *dockerEnvironment,
	entries []offlineAssetEntry,
) error {
	identity := environment.provenance.Testbed
	if identity.ID == "" {
		return fmt.Errorf("clean-room testbed image identity is missing")
	}
	helper := offlineSidecarName(environment.name, "-net-helper")
	args := []string{
		"run", "-d", "--rm", "--pull=never",
		"--network=container:" + environment.name,
		"--cap-drop=ALL", "--cap-add=NET_ADMIN",
		"--device=/dev/net/tun:/dev/net/tun",
		"--security-opt", "no-new-privileges:true",
		"--name", helper,
	}
	args = appendDockerLabels(args, f.Labels)
	caseTimeout := f.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	args = append(args, identity.ID, "sleep", strconv.Itoa(int(caseTimeout.Seconds())+60))
	out, err := environment.commander.Run(ctx, dockerEnv(f.DockerHost), "docker", args...)
	if err != nil {
		return fmt.Errorf("start offline network helper: %w: %s", err, strings.TrimSpace(string(out)))
	}
	environment.sidecars = append(environment.sidecars, helper)
	if err := f.verifyContainerImage(ctx, helper, identity.ID); err != nil {
		return err
	}
	environment.setAuxiliaryImage("network-helper", identity)

	entry, ok := findOfflineAssetEntry(entries, offlineTarpitBinary)
	if !ok {
		return fmt.Errorf("offline tarpit asset is not declared")
	}
	source := filepath.Join(f.OfflineAssetsDir, filepath.FromSlash(entry.path))
	actual, err := regularFileSHA256(source)
	if err != nil {
		return err
	}
	if actual != entry.sha256 {
		return fmt.Errorf("offline tarpit asset changed before copy")
	}
	if err := f.dockerCopy(ctx, source, helper+":"+offlineTarpitContainerPath); err != nil {
		return fmt.Errorf("copy offline tarpit helper: %w", err)
	}
	if err := f.verifyContainerFileSHA256(
		ctx,
		helper,
		offlineTarpitContainerPath,
		entry.sha256,
	); err != nil {
		return fmt.Errorf("verify copied offline tarpit helper: %w", err)
	}
	out, err = environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", helper, "chmod", "0555", offlineTarpitContainerPath,
	)
	if err != nil {
		return fmt.Errorf("make offline tarpit helper executable: %w: %s", err, strings.TrimSpace(string(out)))
	}
	out, err = environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", "-d", helper, offlineTarpitContainerPath,
	)
	if err != nil {
		return fmt.Errorf("launch offline tarpit helper: %w: %s", err, strings.TrimSpace(string(out)))
	}
	healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		_, err := environment.commander.Run(
			healthCtx,
			dockerEnv(f.DockerHost),
			"docker",
			"exec", helper, "test", "-s", offlineTarpitReadyPath,
		)
		if err == nil {
			return nil
		}
		select {
		case <-healthCtx.Done():
			return fmt.Errorf("offline tarpit helper not ready: %w", healthCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func findOfflineAssetEntry(entries []offlineAssetEntry, relative string) (offlineAssetEntry, bool) {
	for _, entry := range entries {
		if entry.path == relative {
			return entry, true
		}
	}
	return offlineAssetEntry{}, false
}

func appendDockerLabels(args []string, labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--label", key+"="+labels[key])
	}
	return args
}

func waitOfflineHTTPBin(ctx context.Context, environment *dockerEnvironment) error {
	healthCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	checks := [][]string{
		{"exec", environment.name, "curl", "-fsS", "--connect-timeout", "2", "http://" + offlineHTTPBinHost + "/get"},
		{"exec", environment.name, "curl", "-fsS", "--cacert", offlineHTTPBinCACertPath, "--connect-timeout", "2", "https://" + offlineHTTPBinHost + "/get"},
	}
	var lastOutput string
	for {
		ready := true
		for _, args := range checks {
			out, err := environment.commander.Run(healthCtx, dockerEnv(environment.dockerHost), "docker", args...)
			lastOutput = strings.TrimSpace(string(out))
			if err != nil {
				ready = false
				break
			}
		}
		if ready {
			return nil
		}
		select {
		case <-healthCtx.Done():
			return fmt.Errorf("offline httpbin sidecar not ready: %w: %s", healthCtx.Err(), lastOutput)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func offlineSidecarName(testbedName, suffix string) string {
	maxBase := maxContainerNameLength - len(suffix)
	if len(testbedName) > maxBase {
		testbedName = testbedName[:maxBase]
	}
	return testbedName + suffix
}

func validateOfflineHTTPBinCerts(certs OfflineHTTPBinCerts) error {
	for label, file := range map[string]string{
		"CA bundle":          certs.CABundle,
		"server certificate": certs.ServerCert,
		"server key":         certs.ServerKey,
	} {
		if strings.TrimSpace(file) == "" {
			return fmt.Errorf("offline httpbin %s path is empty", label)
		}
		if err := requireRegularFileNoSymlink(file); err != nil {
			return fmt.Errorf("offline httpbin %s: %w", label, err)
		}
	}
	return nil
}

// GenerateOfflineHTTPBinCerts creates fixture-only TLS material. Asset
// preparation calls it once and records every generated byte in SHA256SUMS;
// generation runs never create untracked certificate material.
func GenerateOfflineHTTPBinCerts(directory string) (OfflineHTTPBinCerts, error) {
	certs := OfflineHTTPBinCerts{
		CABundle:   filepath.Join(directory, "ca.pem"),
		ServerCert: filepath.Join(directory, "server.crt"),
		ServerKey:  filepath.Join(directory, "server.key"),
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return certs, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "SWE-Bench Offline HTTPBin CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return certs, err
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return certs, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return certs, err
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: offlineHTTPBinHost},
		DNSNames:     []string{offlineHTTPBinHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return certs, err
	}
	if err := writeOfflineHTTPBinPEM(certs.CABundle, "CERTIFICATE", caDER, 0o644); err != nil {
		return certs, err
	}
	if err := writeOfflineHTTPBinPEM(certs.ServerCert, "CERTIFICATE", serverDER, 0o644); err != nil {
		return certs, err
	}
	if err := writeOfflineHTTPBinPEM(certs.ServerKey, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(serverKey), 0o600); err != nil {
		return certs, err
	}
	return certs, nil
}

func writeOfflineHTTPBinPEM(file, blockType string, der []byte, permission os.FileMode) error {
	handle, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, permission)
	if err != nil {
		return err
	}
	defer handle.Close()
	return pem.Encode(handle, &pem.Block{Type: blockType, Bytes: der})
}
