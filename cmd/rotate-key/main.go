// rotate-key re-encrypts all encrypted columns in the database with a new master key.
//
// Usage:
//
//	DATABASE_URL=postgres://... \
//	NETBOX_CONDUCTOR_MASTER_KEY_FILE=/etc/netbox-conductor/master.key \
//	NEW_MASTER_KEY_FILE=/etc/netbox-conductor/master.key.new \
//	  rotate-key
//
// The tool will:
//  1. Load the current master key (from NETBOX_CONDUCTOR_MASTER_KEY_FILE or NETBOX_CONDUCTOR_MASTER_KEY).
//  2. Load or generate the new key (from NEW_MASTER_KEY_FILE or NEW_MASTER_KEY).
//  3. Re-encrypt all encrypted columns in a single transaction.
//  4. Write the new key to NEW_MASTER_KEY_FILE on success (or overwrite the current key file
//     if --in-place is passed).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
)

func main() {
	inPlace := flag.Bool("in-place", false, "overwrite the current key file instead of writing to NEW_MASTER_KEY_FILE")
	flag.Parse()

	_ = godotenv.Load()

	dsn := requireEnv("DATABASE_URL")

	// Load current key
	oldKey, err := crypto.LoadMasterKey(false)
	if err != nil {
		log.Fatalf("loading current master key: %v", err)
	}
	oldEnc := crypto.NewEncryptor(oldKey)

	// Load or generate new key
	newKey, newKeyPath, err := loadOrGenerateNewKey()
	if err != nil {
		log.Fatalf("loading new master key: %v", err)
	}
	newEnc := crypto.NewEncryptor(newKey)

	log.Printf("connecting to database…")
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer conn.Close(ctx)

	log.Printf("starting re-encryption transaction…")
	tx, err := conn.Begin(ctx)
	if err != nil {
		log.Fatalf("beginning transaction: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	total := 0

	// ── credentials.password_enc ─────────────────────────────────────────────
	n, err := reencryptColumn(ctx, tx, oldEnc, newEnc,
		"SELECT id, password_enc FROM credentials WHERE password_enc IS NOT NULL",
		"UPDATE credentials SET password_enc = $1 WHERE id = $2",
	)
	if err != nil {
		log.Fatalf("re-encrypting credentials: %v", err)
	}
	total += n
	log.Printf("  credentials.password_enc: %d rows", n)

	// ── users.totp_secret_enc ────────────────────────────────────────────────
	n, err = reencryptColumn(ctx, tx, oldEnc, newEnc,
		"SELECT id, totp_secret_enc FROM users WHERE totp_secret_enc IS NOT NULL",
		"UPDATE users SET totp_secret_enc = $1 WHERE id = $2",
	)
	if err != nil {
		log.Fatalf("re-encrypting users.totp_secret_enc: %v", err)
	}
	total += n
	log.Printf("  users.totp_secret_enc: %d rows", n)

	// ── clusters.netbox_secret_key ───────────────────────────────────────────
	n, err = reencryptColumn(ctx, tx, oldEnc, newEnc,
		"SELECT id, netbox_secret_key FROM clusters WHERE netbox_secret_key IS NOT NULL",
		"UPDATE clusters SET netbox_secret_key = $1 WHERE id = $2",
	)
	if err != nil {
		log.Fatalf("re-encrypting clusters.netbox_secret_key: %v", err)
	}
	total += n
	log.Printf("  clusters.netbox_secret_key: %d rows", n)

	// ── clusters.api_token_pepper ────────────────────────────────────────────
	n, err = reencryptColumn(ctx, tx, oldEnc, newEnc,
		"SELECT id, api_token_pepper FROM clusters WHERE api_token_pepper IS NOT NULL",
		"UPDATE clusters SET api_token_pepper = $1 WHERE id = $2",
	)
	if err != nil {
		log.Fatalf("re-encrypting clusters.api_token_pepper: %v", err)
	}
	total += n
	log.Printf("  clusters.api_token_pepper: %d rows", n)

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("committing transaction: %v", err)
	}

	log.Printf("re-encryption complete: %d values updated", total)

	// Write new key to disk
	destPath := newKeyPath
	if *inPlace {
		destPath = os.Getenv("NETBOX_CONDUCTOR_MASTER_KEY_FILE")
		if destPath == "" {
			destPath = "/etc/netbox-conductor/master.key"
		}
	}
	newKeyHex := hex.EncodeToString(newKey[:]) + "\n"
	if err := os.WriteFile(destPath, []byte(newKeyHex), 0400); err != nil {
		log.Fatalf("writing new key to %s: %v", destPath, err)
	}
	log.Printf("new master key written to %s", destPath)
	if !*inPlace {
		log.Printf("IMPORTANT: replace your current key file with the new one before restarting the server:\n  mv %s <current-key-file>", destPath)
	}
}

// reencryptColumn reads all rows matching selectSQL, re-encrypts the BYTEA column,
// and applies the update. Returns the number of rows processed.
func reencryptColumn(ctx context.Context, tx pgx.Tx, oldEnc, newEnc *crypto.Encryptor, selectSQL, updateSQL string) (int, error) {
	rows, err := tx.Query(ctx, selectSQL)
	if err != nil {
		return 0, fmt.Errorf("querying rows: %w", err)
	}
	defer rows.Close()

	type row struct {
		id  interface{}
		enc []byte
	}
	var items []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.enc); err != nil {
			return 0, fmt.Errorf("scanning row: %w", err)
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, item := range items {
		if len(item.enc) == 0 {
			continue
		}
		plaintext, err := oldEnc.Decrypt(item.enc)
		if err != nil {
			return 0, fmt.Errorf("decrypting row %v: %w", item.id, err)
		}
		reencrypted, err := newEnc.Encrypt(plaintext)
		if err != nil {
			return 0, fmt.Errorf("re-encrypting row %v: %w", item.id, err)
		}
		if _, err := tx.Exec(ctx, updateSQL, reencrypted, item.id); err != nil {
			return 0, fmt.Errorf("updating row %v: %w", item.id, err)
		}
	}
	return len(items), nil
}

func loadOrGenerateNewKey() (*crypto.MasterKey, string, error) {
	path := os.Getenv("NEW_MASTER_KEY_FILE")
	if path == "" {
		path = os.Getenv("NETBOX_CONDUCTOR_MASTER_KEY_FILE") + ".new"
		if path == ".new" {
			path = "/etc/netbox-conductor/master.key.new"
		}
	}

	if hexKey := os.Getenv("NEW_MASTER_KEY"); hexKey != "" {
		raw, err := hex.DecodeString(hexKey)
		if err != nil || len(raw) != 32 {
			return nil, path, fmt.Errorf("NEW_MASTER_KEY must be a 64-char hex string (32 bytes)")
		}
		var mk crypto.MasterKey
		copy(mk[:], raw)
		return &mk, path, nil
	}

	// Check if the new key file already exists
	if data, err := os.ReadFile(path); err == nil {
		hexStr := string(data)
		if len(hexStr) > 0 && hexStr[len(hexStr)-1] == '\n' {
			hexStr = hexStr[:len(hexStr)-1]
		}
		raw, err := hex.DecodeString(hexStr)
		if err != nil || len(raw) != 32 {
			return nil, path, fmt.Errorf("invalid key in %s", path)
		}
		var mk crypto.MasterKey
		copy(mk[:], raw)
		log.Printf("loaded new key from %s", path)
		return &mk, path, nil
	}

	// Generate a fresh key
	var mk crypto.MasterKey
	if _, err := io.ReadFull(rand.Reader, mk[:]); err != nil {
		return nil, path, fmt.Errorf("generating new key: %w", err)
	}
	log.Printf("generated new master key (will be written to %s on success)", path)
	return &mk, path, nil
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}
