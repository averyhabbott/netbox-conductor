package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	keySize   = 32 // AES-256
	nonceSize = 12 // GCM standard nonce
)

var ErrInvalidCiphertext = errors.New("invalid ciphertext")

// MasterKey holds the 32-byte AES-256 master key.
type MasterKey [keySize]byte

// LoadMasterKey loads the master key from the path in NETBOX_TOOL_MASTER_KEY_FILE,
// falling back to NETBOX_TOOL_MASTER_KEY (hex-encoded env var).
// If neither is set and create is true, a new key is generated and written to the file.
func LoadMasterKey(create bool) (*MasterKey, error) {
	if path := os.Getenv("NETBOX_TOOL_MASTER_KEY_FILE"); path != "" {
		return loadFromFile(path, create)
	}
	if hexKey := os.Getenv("NETBOX_TOOL_MASTER_KEY"); hexKey != "" {
		return loadFromHex(hexKey)
	}
	// Default path
	return loadFromFile("/etc/netbox-tool/master.key", create)
}

func loadFromFile(path string, create bool) (*MasterKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && create {
			return generateAndSave(path)
		}
		return nil, fmt.Errorf("reading master key file %s: %w", path, err)
	}
	hexStr := string(data)
	// Strip trailing newline if present
	if len(hexStr) > 0 && hexStr[len(hexStr)-1] == '\n' {
		hexStr = hexStr[:len(hexStr)-1]
	}
	return loadFromHex(hexStr)
}

func loadFromHex(hexStr string) (*MasterKey, error) {
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("decoding master key hex: %w", err)
	}
	if len(raw) != keySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", keySize, len(raw))
	}
	var mk MasterKey
	copy(mk[:], raw)
	return &mk, nil
}

func generateAndSave(path string) (*MasterKey, error) {
	var mk MasterKey
	if _, err := io.ReadFull(rand.Reader, mk[:]); err != nil {
		return nil, fmt.Errorf("generating master key: %w", err)
	}
	hexKey := hex.EncodeToString(mk[:]) + "\n"
	if err := os.WriteFile(path, []byte(hexKey), 0400); err != nil {
		return nil, fmt.Errorf("writing master key to %s: %w", path, err)
	}
	return &mk, nil
}

// Encryptor performs AES-256-GCM encryption/decryption using the master key.
type Encryptor struct {
	key [keySize]byte
}

// NewEncryptor creates an Encryptor from a MasterKey.
func NewEncryptor(mk *MasterKey) *Encryptor {
	return &Encryptor{key: *mk}
}

// Encrypt encrypts plaintext and returns nonce||ciphertext||tag as a single byte slice.
// The result is safe to store directly in a BYTEA column.
func (e *Encryptor) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.key[:])
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	// Seal appends ciphertext+tag after nonce
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt decrypts a byte slice produced by Encrypt.
func (e *Encryptor) Decrypt(data []byte) ([]byte, error) {
	if len(data) < nonceSize {
		return nil, ErrInvalidCiphertext
	}

	block, err := aes.NewCipher(e.key[:])
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonce := data[:nonceSize]
	ciphertext := data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCiphertext, err)
	}
	return plaintext, nil
}

// EncryptString is a convenience wrapper for string values.
func (e *Encryptor) EncryptString(s string) ([]byte, error) {
	return e.Encrypt([]byte(s))
}

// DecryptString is a convenience wrapper that returns a string.
func (e *Encryptor) DecryptString(data []byte) (string, error) {
	b, err := e.Decrypt(data)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// HashToken computes SHA-256 of a raw token and returns the hex string.
// Used for storing agent tokens and refresh tokens without keeping plaintext.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// GenerateToken generates a cryptographically random token of byteLen bytes,
// returned as a base64url-encoded string (URL-safe, no padding).
func GenerateToken(byteLen int) (string, error) {
	raw := make([]byte, byteLen)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generating random token: %w", err)
	}
	// Use hex encoding for simplicity and compatibility with env files
	return hex.EncodeToString(raw), nil
}
