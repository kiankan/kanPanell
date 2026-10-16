package service

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AutoInboundTLSConfig is the operator-supplied TLS material used by generated
// VLESS inbounds. Private-key contents are never stored in the panel database;
// only paths are persisted.
type AutoInboundTLSConfig struct {
	Domain          string `json:"domain"`
	CertificateFile string `json:"certificateFile"`
	KeyFile         string `json:"keyFile"`
}

// Validate checks the domain, certificate validity, hostname match, and key
// pair before any automatically managed inbound may be created.
func (c AutoInboundTLSConfig) Validate() error {
	domain := strings.TrimSpace(strings.TrimSuffix(c.Domain, "."))
	if !validAutoInboundDomain(domain) || strings.ContainsAny(domain, " /\\\\:@?#") || net.ParseIP(domain) != nil {
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
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		return fmt.Errorf("cannot load system trust roots to validate Auto Inbound certificate")
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		cert, parseErr := x509.ParseCertificate(der)
		if parseErr != nil {
			return fmt.Errorf("cannot parse Auto Inbound intermediate certificate: %w", parseErr)
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName:       domain,
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime:   now,
	}); err != nil {
		return fmt.Errorf("Auto Inbound certificate chain is not trusted for domain %q: %w", domain, err)
	}
	return nil
}

func validAutoInboundDomain(domain string) bool {
	if !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func readableRegularFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
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
