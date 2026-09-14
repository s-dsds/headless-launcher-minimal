package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClientEndpoint(t *testing.T) {
	for _, value := range []string{"http://user:pass@127.0.0.1", "http://127.0.0.1/path", "http://127.0.0.1?token=x", "ftp://rooms.example.com", "https://"} {
		t.Setenv("WLHL_URL", value)
		if _, err := endpoint(); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	for _, value := range []string{"http://127.0.0.1:8787", "http://192.0.2.10:8787", "https://rooms.example.com"} {
		t.Setenv("WLHL_URL", value)
		if _, err := endpoint(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTLSFiles(t *testing.T) {
	if config, err := loadTLSConfig("", ""); config != nil || err != nil {
		t.Fatal("plain HTTP should need no files")
	}
	if _, err := loadTLSConfig("cert.pem", ""); err == nil {
		t.Fatal("accepted missing key")
	}
	if _, err := loadTLSConfig("missing-cert.pem", "missing-key.pem"); err == nil {
		t.Fatal("accepted missing certificate")
	}
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadTLSConfig(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS12 || len(config.Certificates) != 1 {
		t.Fatal("TLS not configured")
	}
}
