//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	managedHTTPBinContainer = "swebench-managed-httpbin"
	managedHTTPBinImage     = "kennethreitz/httpbin"
	managedHTTPBinHost      = "httpbin.org"
	managedHTTPBinHTTPPort  = 80
	managedHTTPBinHTTPSPort = 443
	managedHTTPBinBackPort  = 18081
)

type managedHTTPBinInfo struct {
	Enabled          bool   `json:"enabled"`
	BackendContainer string `json:"backend_container"`
	BackendImage     string `json:"backend_image"`
	BackendURL       string `json:"backend_url"`
	PublicHost       string `json:"public_host"`
	HTTPPort         int    `json:"http_port"`
	HTTPSPort        int    `json:"https_port"`
	CABundle         string `json:"ca_bundle"`
	CertDir          string `json:"cert_dir"`
}

type managedHTTPBin struct {
	Info        managedHTTPBinInfo
	httpServer  *http.Server
	httpsServer *http.Server
	httpLn      net.Listener
	httpsLn     net.Listener
}

func ensureManagedHTTPBin(ctx context.Context, docker, dockerHost string) (*managedHTTPBin, error) {
	baseDir := absPath(filepath.Join("results", "runs", "managed-httpbin"))
	certDir := filepath.Join(baseDir, "certs")
	certs, err := ensureManagedHTTPBinCerts(certDir)
	if err != nil {
		return nil, err
	}
	if err := ensureManagedHTTPBinBackend(ctx, docker, dockerHost); err != nil {
		return nil, err
	}
	if err := waitHTTPBinBackend(ctx, managedHTTPBinBackendURL()); err != nil {
		return nil, err
	}
	managed, err := startManagedHTTPBinProxy(certs)
	if err != nil {
		return nil, err
	}
	managed.Info = managedHTTPBinInfo{
		Enabled:          true,
		BackendContainer: managedHTTPBinContainer,
		BackendImage:     managedHTTPBinImage,
		BackendURL:       managedHTTPBinBackendURL(),
		PublicHost:       managedHTTPBinHost,
		HTTPPort:         managedHTTPBinHTTPPort,
		HTTPSPort:        managedHTTPBinHTTPSPort,
		CABundle:         certs.CABundle,
		CertDir:          certDir,
	}
	return managed, nil
}

func managedHTTPBinBackendURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", managedHTTPBinBackPort)
}

func ensureManagedHTTPBinBackend(ctx context.Context, docker, dockerHost string) error {
	env := []string{"DOCKER_HOST=" + dockerHost}
	inspect := runCapture(ctx, "", env, docker, "inspect", "-f", "{{.State.Running}}", managedHTTPBinContainer)
	if inspect.ExitCode == 0 {
		if strings.TrimSpace(inspect.Stdout) == "true" {
			return nil
		}
		start := runCapture(ctx, "", env, docker, "start", managedHTTPBinContainer)
		if start.ExitCode != 0 {
			return fmt.Errorf("start managed httpbin container: %s", strings.TrimSpace(start.Stderr+"\n"+start.Stdout))
		}
		return nil
	}
	run := runCapture(ctx, "", env, docker,
		"run", "-d",
		"--name", managedHTTPBinContainer,
		"-p", fmt.Sprintf("127.0.0.1:%d:80", managedHTTPBinBackPort),
		managedHTTPBinImage,
	)
	if run.ExitCode != 0 {
		return fmt.Errorf("create managed httpbin container: %s", strings.TrimSpace(run.Stderr+"\n"+run.Stdout))
	}
	return nil
}

func waitHTTPBinBackend(ctx context.Context, baseURL string) error {
	client := http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/get", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("status=%d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("managed httpbin backend not ready: %v", lastErr)
}

func startManagedHTTPBinProxy(certs managedHTTPBinCerts) (*managedHTTPBin, error) {
	target, err := url.Parse(managedHTTPBinBackendURL())
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	httpLn, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", managedHTTPBinHTTPPort))
	if err != nil {
		return nil, fmt.Errorf("listen managed httpbin HTTP port %d: %w", managedHTTPBinHTTPPort, err)
	}
	httpsLn, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", managedHTTPBinHTTPSPort))
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("listen managed httpbin HTTPS port %d: %w", managedHTTPBinHTTPSPort, err)
	}
	cert, err := tls.LoadX509KeyPair(certs.ServerCert, certs.ServerKey)
	if err != nil {
		_ = httpLn.Close()
		_ = httpsLn.Close()
		return nil, err
	}

	managed := &managedHTTPBin{
		httpLn:  httpLn,
		httpsLn: httpsLn,
		httpServer: &http.Server{
			Handler: proxy,
		},
		httpsServer: &http.Server{
			Handler: proxy,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			},
		},
	}
	go func() {
		if err := managed.httpServer.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "managed httpbin HTTP proxy stopped: %v\n", err)
		}
	}()
	go func() {
		if err := managed.httpsServer.ServeTLS(httpsLn, "", ""); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "managed httpbin HTTPS proxy stopped: %v\n", err)
		}
	}()
	return managed, nil
}

func (m *managedHTTPBin) Close(ctx context.Context) {
	if m == nil {
		return
	}
	if m.httpServer != nil {
		_ = m.httpServer.Shutdown(ctx)
	}
	if m.httpsServer != nil {
		_ = m.httpsServer.Shutdown(ctx)
	}
	if m.httpLn != nil {
		_ = m.httpLn.Close()
	}
	if m.httpsLn != nil {
		_ = m.httpsLn.Close()
	}
}

type managedHTTPBinCerts struct {
	CACert     string
	CAKey      string
	ServerCert string
	ServerKey  string
	CABundle   string
}

func ensureManagedHTTPBinCerts(certDir string) (managedHTTPBinCerts, error) {
	certs := managedHTTPBinCerts{
		CACert:     filepath.Join(certDir, "ca.crt"),
		CAKey:      filepath.Join(certDir, "ca.key"),
		ServerCert: filepath.Join(certDir, "server.crt"),
		ServerKey:  filepath.Join(certDir, "server.key"),
		CABundle:   filepath.Join(certDir, "ca-bundle.pem"),
	}
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return certs, err
	}
	if filesExist(certs.CACert, certs.CAKey, certs.ServerCert, certs.ServerKey, certs.CABundle) {
		return certs, nil
	}

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return certs, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "SWE-Bench Managed HTTPBin CA"},
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
		Subject:      pkix.Name{CommonName: managedHTTPBinHost},
		DNSNames:     []string{managedHTTPBinHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return certs, err
	}

	if err := writePEMFile(certs.CACert, "CERTIFICATE", caDER, 0o644); err != nil {
		return certs, err
	}
	if err := writePrivateKey(certs.CAKey, caKey); err != nil {
		return certs, err
	}
	if err := writePEMFile(certs.ServerCert, "CERTIFICATE", serverDER, 0o644); err != nil {
		return certs, err
	}
	if err := writePrivateKey(certs.ServerKey, serverKey); err != nil {
		return certs, err
	}
	caData, err := os.ReadFile(certs.CACert)
	if err != nil {
		return certs, err
	}
	if err := os.WriteFile(certs.CABundle, caData, 0o644); err != nil {
		return certs, err
	}
	return certs, nil
}

func filesExist(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func writePEMFile(path, typ string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: typ, Bytes: der})
}

func writePrivateKey(path string, key *rsa.PrivateKey) error {
	return writePEMFile(path, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0o600)
}
