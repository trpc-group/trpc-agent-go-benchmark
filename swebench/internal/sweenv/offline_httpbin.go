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
	"sync"
	"time"
)

const (
	offlineHTTPBinHost       = "httpbin.org"
	offlineHTTPBinImage      = "kennethreitz/httpbin"
	offlineHTTPBinCACertPath = "/tmp/tag-swebench-httpbin-ca.pem"
	offlineHTTPBinCertPath   = "/tmp/tag-swebench-httpbin-server.crt"
	offlineHTTPBinKeyPath    = "/tmp/tag-swebench-httpbin-server.key"
)

// OfflineHTTPBinCerts contains the local CA and server certificate copied into
// an isolated requests testbed and its loopback-only httpbin sidecar.
type OfflineHTTPBinCerts struct {
	CABundle   string
	ServerCert string
	ServerKey  string
}

var (
	defaultOfflineHTTPBinCerts     OfflineHTTPBinCerts
	defaultOfflineHTTPBinCertsErr  error
	defaultOfflineHTTPBinCertsOnce sync.Once
)

func usesOfflineHTTPBin(instanceID string) bool {
	return strings.HasPrefix(instanceID, "psf__requests-")
}

func (f DockerFactory) startOfflineHTTPBin(ctx context.Context, environment *dockerEnvironment) error {
	certs := f.HTTPBinCerts
	if certs == nil {
		generated, err := ensureDefaultOfflineHTTPBinCerts()
		if err != nil {
			return fmt.Errorf("prepare offline httpbin certificates: %w", err)
		}
		certs = &generated
	}
	if err := validateOfflineHTTPBinCerts(*certs); err != nil {
		return err
	}
	if err := f.dockerCopy(ctx, certs.CABundle, environment.name+":"+offlineHTTPBinCACertPath); err != nil {
		return fmt.Errorf("copy offline httpbin CA into testbed: %w", err)
	}

	sidecar := offlineHTTPBinSidecarName(environment.name)
	args := []string{
		"run", "-d", "--rm",
		"--pull=never",
		"--network=container:" + environment.name,
		"--name", sidecar,
	}
	labelKeys := make([]string, 0, len(f.Labels))
	for key := range f.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		args = append(args, "--label", key+"="+f.Labels[key])
	}
	caseTimeout := f.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	args = append(args, offlineHTTPBinImage, "sleep", strconv.Itoa(int(caseTimeout.Seconds())+60))
	out, err := environment.commander.Run(ctx, dockerEnv(f.DockerHost), "docker", args...)
	if err != nil {
		return fmt.Errorf("start offline httpbin sidecar: %w: %s", err, strings.TrimSpace(string(out)))
	}
	environment.sidecars = append(environment.sidecars, sidecar)

	if err := f.dockerCopy(ctx, certs.ServerCert, sidecar+":"+offlineHTTPBinCertPath); err != nil {
		return fmt.Errorf("copy offline httpbin server certificate: %w", err)
	}
	if err := f.dockerCopy(ctx, certs.ServerKey, sidecar+":"+offlineHTTPBinKeyPath); err != nil {
		return fmt.Errorf("copy offline httpbin server key: %w", err)
	}
	serviceCommand := "gunicorn -b 127.0.0.1:80 httpbin:app -k gevent " +
		">/tmp/tag-swebench-httpbin-http.log 2>&1 & " +
		"exec gunicorn -b 127.0.0.1:443 httpbin:app -k gevent " +
		"--certfile " + offlineHTTPBinCertPath + " --keyfile " + offlineHTTPBinKeyPath + " " +
		">/tmp/tag-swebench-httpbin-https.log 2>&1"
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

func (e *dockerEnvironment) setExtraEnv(values map[string]string) {
	if e.extraEnv == nil {
		e.extraEnv = make(map[string]string, len(values))
	}
	for key, value := range values {
		e.extraEnv[key] = value
	}
}

func (f DockerFactory) dockerCopy(ctx context.Context, source, destination string) error {
	commander := f.Commander
	if commander == nil {
		commander = osCommander{}
	}
	out, err := commander.Run(ctx, dockerEnv(f.DockerHost), "docker", "cp", source, destination)
	if err != nil {
		return fmt.Errorf("docker cp %s: %w: %s", destination, err, strings.TrimSpace(string(out)))
	}
	return nil
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

func offlineHTTPBinSidecarName(testbedName string) string {
	const suffix = "-httpbin"
	maxBase := 63 - len(suffix)
	if len(testbedName) > maxBase {
		testbedName = testbedName[:maxBase]
	}
	return testbedName + suffix
}

func validateOfflineHTTPBinCerts(certs OfflineHTTPBinCerts) error {
	for label, path := range map[string]string{
		"CA bundle":          certs.CABundle,
		"server certificate": certs.ServerCert,
		"server key":         certs.ServerKey,
	} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("offline httpbin %s path is empty", label)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("stat offline httpbin %s %s: %w", label, path, err)
		}
	}
	return nil
}

func ensureDefaultOfflineHTTPBinCerts() (OfflineHTTPBinCerts, error) {
	defaultOfflineHTTPBinCertsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "tag-swebench-httpbin-certs-")
		if err != nil {
			defaultOfflineHTTPBinCertsErr = err
			return
		}
		defaultOfflineHTTPBinCerts, defaultOfflineHTTPBinCertsErr = generateOfflineHTTPBinCerts(dir)
	})
	return defaultOfflineHTTPBinCerts, defaultOfflineHTTPBinCertsErr
}

func generateOfflineHTTPBinCerts(dir string) (OfflineHTTPBinCerts, error) {
	certs := OfflineHTTPBinCerts{
		CABundle:   filepath.Join(dir, "ca.pem"),
		ServerCert: filepath.Join(dir, "server.crt"),
		ServerKey:  filepath.Join(dir, "server.key"),
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return certs, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "TAG SWE-Bench Offline HTTPBin CA"},
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

func writeOfflineHTTPBinPEM(path, blockType string, der []byte, permission os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, permission)
	if err != nil {
		return err
	}
	defer file.Close()
	return pem.Encode(file, &pem.Block{Type: blockType, Bytes: der})
}
