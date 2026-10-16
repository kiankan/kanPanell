package service

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// AutoInboundTLSConfig is the operator-supplied TLS material used by generated
// VLESS inbounds. Private-key contents are never stored in the panel database;
// only paths are persisted.
type AutoInboundTLSConfig struct {
	Domain         string `json:"domain"`
	CertificateFile string `json:"certificateFile"`
	KeyFile         string `json:"keyFile"`
}

// Validate checks the domain, certificate validity, hostname match, and key
// pair before any automatically managed inbound may be created.
func (c AutoInboundTLSConfig) Validate() error {
	domain := strings.TrimSpace(strings.TrimSuffix(c.Domain, "."))
	if domain == "" || strings.ContainsAny(domain, " /\\\\:@?#") || net.ParseIP(domain) != nil {
		return fmt.Errorf("a valid DNS domain is required for Auto Inbound TLS")
	}
	if strings.TrimSpace(c.CertificateFile) == "" || strings.TrimSpace(c.KeyFile) == "" {
		return fmt.Errorf("Auto Inbound TLS certificate and private-key paths are required")
	}
	certPath, err := readableRegularFile(c.CertificateFile)
	if err != nil {
		return fmt.Errorf("invalid Auto Inbound certificate file: %w", err)
	}
	keyPath, err := readableRegularFile(c.KeyFile)
	if err != nil {
		return fmt.Errorf("invalid Auto Inbound private-key file: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("Auto Inbound certificate and private key are not a valid matching pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return fmt.Errorf("Auto Inbound certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("cannot parse Auto Inbound leaf certificate: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return fmt.Errorf("Auto Inbound certificate is not currently valid")
	}
	if err := leaf.VerifyHostname(domain); err != nil {
		return fmt.Errorf("Auto Inbound certificate does not cover domain %q: %w", domain, err)
	}
	return nil
}

func readableRegularFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	return path, f.Close()
}
