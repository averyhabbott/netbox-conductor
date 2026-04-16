// Package tlscert manages the Conductor's TLS certificate.
//
// At startup, EnsureExists is called with the desired cert and key paths.
// If the files do not exist, or the cert expires within 30 days, a new
// self-signed ECDSA P-256 certificate is generated and written. The cert
// is its own CA (IsCA: true), so agents can trust it by pointing
// AGENT_TLS_CA_CERT at the same file.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Regenerate forcibly creates a new self-signed certificate, overwriting any
// existing files at certFile and keyFile. Unlike EnsureExists it does not check
// whether the existing cert is still valid.
func Regenerate(certFile, keyFile string, dnsNames []string, ipAddrs []net.IP) error {
	return generate(certFile, keyFile, dnsNames, ipAddrs)
}

// EnsureExists checks whether certFile and keyFile exist and are not expiring
// within 30 days. If either condition fails, a new self-signed certificate is
// generated with the provided SANs and written to both paths.
//
// The containing directory is created (mode 0755) if it does not exist.
func EnsureExists(certFile, keyFile string, dnsNames []string, ipAddrs []net.IP) (generated bool, err error) {
	if exists(certFile) && exists(keyFile) && !expiringSoon(certFile) {
		return false, nil
	}
	return true, generate(certFile, keyFile, dnsNames, ipAddrs)
}

// SANsFromServerURL parses serverURL and extracts the hostname or IP for the
// SAN list. localhost and 127.0.0.1 are always included.
func SANsFromServerURL(serverURL string) (dnsNames []string, ipAddrs []net.IP) {
	dnsNames = []string{"localhost"}
	ipAddrs = []net.IP{net.ParseIP("127.0.0.1")}

	if serverURL == "" {
		return
	}
	u, err := url.Parse(serverURL)
	if err != nil {
		return
	}
	host := u.Hostname()
	if host == "" || host == "localhost" {
		return
	}
	if ip := net.ParseIP(host); ip != nil {
		ipAddrs = append(ipAddrs, ip)
	} else {
		dnsNames = append(dnsNames, host)
	}
	return
}

// generate creates a new self-signed cert and writes it to certFile / keyFile.
func generate(certFile, keyFile string, dnsNames []string, ipAddrs []net.IP) error {
	if err := os.MkdirAll(filepath.Dir(certFile), 0755); err != nil {
		return fmt.Errorf("creating cert directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), 0755); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}

	// Generate private key (ECDSA P-256)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating private key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generating serial number: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "netbox-conductor",
			Organization: []string{"NetBox Conductor"},
		},
		NotBefore:             now.Add(-time.Minute), // small back-date for clock skew
		NotAfter:              now.Add(2 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // agents trust this cert as their CA root
		DNSNames:              dnsNames,
		IPAddresses:           ipAddrs,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating certificate: %w", err)
	}

	// Write cert (world-readable — agents need to download it)
	if err := writePEM(certFile, "CERTIFICATE", certDER, 0644); err != nil {
		return err
	}

	// Write key (owner-readable only)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshaling private key: %w", err)
	}
	return writePEM(keyFile, "EC PRIVATE KEY", keyDER, 0600)
}

func writePEM(path, blockType string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: data})
}

// CertInfo holds human-readable information about a TLS certificate.
type CertInfo struct {
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	DNSNames    []string  `json:"dns_names"`
	IPAddresses []string  `json:"ip_addresses"`
	Fingerprint string    `json:"fingerprint"` // hex-encoded SHA-256
}

// ReadCertInfo parses the PEM cert at certFile and returns metadata.
// Returns nil, nil if the file does not exist (TLS disabled).
func ReadCertInfo(certFile string) (*CertInfo, error) {
	if certFile == "" {
		return nil, nil
	}
	pemData, err := os.ReadFile(certFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", certFile)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing cert: %w", err)
	}

	ips := make([]string, len(cert.IPAddresses))
	for i, ip := range cert.IPAddresses {
		ips[i] = ip.String()
	}

	sum := sha256.Sum256(cert.Raw)
	fp := fmt.Sprintf("%x", sum[:])

	return &CertInfo{
		Subject:     cert.Subject.CommonName,
		NotBefore:   cert.NotBefore,
		NotAfter:    cert.NotAfter,
		DNSNames:    cert.DNSNames,
		IPAddresses: ips,
		Fingerprint: fp,
	}, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// expiringSoon returns true if the cert at path expires within 30 days,
// or if it cannot be read / parsed.
func expiringSoon(certFile string) bool {
	pemData, err := os.ReadFile(certFile)
	if err != nil {
		return true
	}
	block, _ := pem.Decode(pemData)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return time.Until(cert.NotAfter) < 30*24*time.Hour
}
