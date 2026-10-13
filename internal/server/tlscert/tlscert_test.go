package tlscert

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureExistsKeepsExistingCert(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")

	if generated, err := EnsureExists(certFile, keyFile, nil, nil); err != nil || !generated {
		t.Fatalf("first run: generated=%v err=%v, want a new cert", generated, err)
	}
	before, _ := os.ReadFile(certFile)

	if generated, err := EnsureExists(certFile, keyFile, nil, nil); err != nil || generated {
		t.Fatalf("second run: generated=%v err=%v, want existing cert kept", generated, err)
	}
	if after, _ := os.ReadFile(certFile); !bytes.Equal(before, after) {
		t.Fatal("existing cert was replaced")
	}
}
