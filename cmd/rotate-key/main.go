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
//  3. Save a newly generated key to NEW_MASTER_KEY_FILE before touching the database,
//     so the key that encrypts the data is always on disk.
//  4. Re-encrypt all encrypted columns in a single transaction.
//  5. With --in-place, move the new key file over the current key file after commit.
//
// Stop the netbox-conductor server first: anything it encrypts during the rotation
// would use the old key and become unreadable afterwards.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/jackc/pgx/v5"
)

func main() {
	inPlace := flag.Bool("in-place", false, "overwrite the current key file instead of writing to NEW_MASTER_KEY_FILE")
	flag.Parse()

	dsn := requireEnv("DATABASE_URL")

	// Load current key
	oldKey, err := crypto.LoadMasterKey(false)
	if err != nil {
		log.Fatalf("loading current master key: %v", err)
	}
	oldEnc := crypto.NewEncryptor(oldKey)

	// Load or generate new key
	newKey, newKeyPath, fromFile, err := loadOrGenerateNewKey()
	if err != nil {
		log.Fatalf("loading new master key: %v", err)
	}
	newEnc := crypto.NewEncryptor(newKey)
	if !fromFile {
		if err := writeKeyFile(newKeyPath, newKey); err != nil {
			log.Fatalf("saving new key to %s (database untouched): %v", newKeyPath, err)
		}
		log.Printf("new master key saved to %s", newKeyPath)
	}

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
		false,
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
		false,
	)
	if err != nil {
		log.Fatalf("re-encrypting users.totp_secret_enc: %v", err)
	}
	total += n
	log.Printf("  users.totp_secret_enc: %d rows", n)

	// ── backup_targets.*_enc (base64 text) ───────────────────────────────────
	for _, col := range []string{"s3_key_id_enc", "s3_secret_enc", "gcs_key_enc", "azure_key_enc", "sftp_private_key_enc"} {
		n, err = reencryptColumn(ctx, tx, oldEnc, newEnc,
			"SELECT id, "+col+" FROM backup_targets WHERE "+col+" IS NOT NULL",
			"UPDATE backup_targets SET "+col+" = $1 WHERE id = $2",
			true,
		)
		if err != nil {
			log.Fatalf("re-encrypting backup_targets.%s: %v", col, err)
		}
		total += n
		log.Printf("  backup_targets.%s: %d rows", col, n)
	}

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("committing transaction: %v", err)
	}

	log.Printf("re-encryption complete: %d values updated", total)

	if !*inPlace {
		log.Printf("IMPORTANT: replace your current key file with the new one before restarting the server:\n  mv %s <current-key-file>", newKeyPath)
		return
	}
	destPath := os.Getenv("NETBOX_CONDUCTOR_MASTER_KEY_FILE")
	if destPath == "" {
		destPath = "/etc/netbox-conductor/master.key"
	}
	if err := os.Rename(newKeyPath, destPath); err != nil {
		log.Fatalf("database now uses the key in %s, but moving it to %s failed: %v — move it by hand before restarting the server", newKeyPath, destPath, err)
	}
	log.Printf("new master key moved to %s", destPath)
}

// writeKeyFile writes the key as hex and syncs it to disk. Fails if the file exists.
func writeKeyFile(path string, key *crypto.MasterKey) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(hex.EncodeToString(key[:]) + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// reencryptColumn reads all rows matching selectSQL, re-encrypts the column, and
// applies the update. b64 marks TEXT columns holding base64 ciphertext (backup
// targets) rather than BYTEA. Returns the number of rows processed.
func reencryptColumn(ctx context.Context, tx pgx.Tx, oldEnc, newEnc *crypto.Encryptor, selectSQL, updateSQL string, b64 bool) (int, error) {
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
		ciphertext := item.enc
		if b64 {
			if ciphertext, err = base64.StdEncoding.DecodeString(string(item.enc)); err != nil {
				return 0, fmt.Errorf("decoding row %v: %w", item.id, err)
			}
		}
		plaintext, err := oldEnc.Decrypt(ciphertext)
		if err != nil {
			return 0, fmt.Errorf("decrypting row %v: %w", item.id, err)
		}
		reencrypted, err := newEnc.Encrypt(plaintext)
		if err != nil {
			return 0, fmt.Errorf("re-encrypting row %v: %w", item.id, err)
		}
		var newVal any = reencrypted
		if b64 {
			newVal = base64.StdEncoding.EncodeToString(reencrypted)
		}
		if _, err := tx.Exec(ctx, updateSQL, newVal, item.id); err != nil {
			return 0, fmt.Errorf("updating row %v: %w", item.id, err)
		}
	}
	return len(items), nil
}

func loadOrGenerateNewKey() (key *crypto.MasterKey, path string, fromFile bool, err error) {
	path = os.Getenv("NEW_MASTER_KEY_FILE")
	if path == "" {
		path = os.Getenv("NETBOX_CONDUCTOR_MASTER_KEY_FILE") + ".new"
		if path == ".new" {
			path = "/etc/netbox-conductor/master.key.new"
		}
	}

	if hexKey := os.Getenv("NEW_MASTER_KEY"); hexKey != "" {
		raw, err := hex.DecodeString(hexKey)
		if err != nil || len(raw) != 32 {
			return nil, path, false, fmt.Errorf("NEW_MASTER_KEY must be a 64-char hex string (32 bytes)")
		}
		var mk crypto.MasterKey
		copy(mk[:], raw)
		return &mk, path, false, nil
	}

	// Check if the new key file already exists
	if data, err := os.ReadFile(path); err == nil {
		hexStr := string(data)
		if len(hexStr) > 0 && hexStr[len(hexStr)-1] == '\n' {
			hexStr = hexStr[:len(hexStr)-1]
		}
		raw, err := hex.DecodeString(hexStr)
		if err != nil || len(raw) != 32 {
			return nil, path, false, fmt.Errorf("invalid key in %s", path)
		}
		var mk crypto.MasterKey
		copy(mk[:], raw)
		log.Printf("loaded new key from %s", path)
		return &mk, path, true, nil
	}

	// Generate a fresh key
	var mk crypto.MasterKey
	if _, err := io.ReadFull(rand.Reader, mk[:]); err != nil {
		return nil, path, false, fmt.Errorf("generating new key: %w", err)
	}
	log.Printf("generated new master key")
	return &mk, path, false, nil
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}
