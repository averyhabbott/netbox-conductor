package config

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/averyhabbott/netbox-conductor/internal/server/tlscert"
)

func TestCheckCertPEM(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, err := tlscert.EnsureExists(certFile, keyFile, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(certFile)
	key, _ := os.ReadFile(keyFile)

	if err := checkCertPEM(good); err != nil {
		t.Errorf("real cert rejected: %v", err)
	}
	for name, data := range map[string][]byte{
		"empty":       nil,
		"html page":   []byte("<html><body>Login</body></html>"),
		"private key": key,
		"bad body":    []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"),
	} {
		if err := checkCertPEM(data); err == nil {
			t.Errorf("%s accepted as a certificate", name)
		}
	}
}
