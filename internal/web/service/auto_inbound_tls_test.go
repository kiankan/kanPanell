package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoInboundTLSConfigRejectsMissingValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  AutoInboundTLSConfig
		want string
	}{
		{name: "empty", cfg: AutoInboundTLSConfig{}, want: "valid DNS domain"},
		{name: "ip address", cfg: AutoInboundTLSConfig{Domain: "127.0.0.1"}, want: "valid DNS domain"},
		{name: "missing certificate", cfg: AutoInboundTLSConfig{Domain: "vpn.example.com", KeyFile: "/tmp/key.pem"}, want: "paths are required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestAutoInboundTLSConfigRejectsUnreadableOrInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	key := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("not a private key"), 0600); err != nil {
		t.Fatal(err)
	}
	err := (AutoInboundTLSConfig{Domain: "vpn.example.com", CertificateFile: cert, KeyFile: key}).Validate()
	if err == nil || !strings.Contains(err.Error(), "matching pair") {
		t.Fatalf("Validate() error = %v, want invalid matching-pair error", err)
	}
}

func TestReadableRegularFileRejectsDirectory(t *testing.T) {
	_, err := readableRegularFile(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("readableRegularFile(directory) error = %v, want regular-file error", err)
	}
}
