package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User represents a row in the users table.
type User struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
	Role         string
	TOTPSecretEnc []byte
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// RefreshToken represents a row in the refresh_tokens table.
type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TokenHash string
	IssuedAt  time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// UserQuerier performs user-related DB operations.
type UserQuerier struct {
	pool *pgxpool.Pool
}

func NewUserQuerier(pool *pgxpool.Pool) *UserQuerier {
	return &UserQuerier{pool: pool}
}

func (q *UserQuerier) GetByUsername(ctx context.Context, username string) (*User, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, username, password_hash, role, totp_secret_enc, created_at, last_login_at
		FROM users WHERE username = $1
	`, username)

	var u User
	if err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role,
		&u.TOTPSecretEnc, &u.CreatedAt, &u.LastLoginAt,
	); err != nil {
		return nil, err
	}
	return &u, nil
}

func (q *UserQuerier) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, username, password_hash, role, totp_secret_enc, created_at, last_login_at
		FROM users WHERE id = $1
	`, id)

	var u User
	if err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role,
		&u.TOTPSecretEnc, &u.CreatedAt, &u.LastLoginAt,
	); err != nil {
		return nil, err
	}
	return &u, nil
}

func (q *UserQuerier) Create(ctx context.Context, username, passwordHash, role string) (*User, error) {
	row := q.pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING id, username, password_hash, role, totp_secret_enc, created_at, last_login_at
	`, username, passwordHash, role)

	var u User
	if err := row.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role,
		&u.TOTPSecretEnc, &u.CreatedAt, &u.LastLoginAt,
	); err != nil {
		return nil, err
	}
	return &u, nil
}

func (q *UserQuerier) List(ctx context.Context) ([]User, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, username, password_hash, role, totp_secret_enc, created_at, last_login_at
		FROM users ORDER BY username
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(
			&u.ID, &u.Username, &u.PasswordHash, &u.Role,
			&u.TOTPSecretEnc, &u.CreatedAt, &u.LastLoginAt,
		); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (q *UserQuerier) UpdateLastLogin(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx,
		`UPDATE users SET last_login_at = now() WHERE id = $1`, id)
	return err
}

func (q *UserQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// RefreshTokenQuerier handles refresh token storage.
type RefreshTokenQuerier struct {
	pool *pgxpool.Pool
}

func NewRefreshTokenQuerier(pool *pgxpool.Pool) *RefreshTokenQuerier {
	return &RefreshTokenQuerier{pool: pool}
}

func (q *RefreshTokenQuerier) Create(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	_, err := q.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, userID, tokenHash, expiresAt)
	return err
}

func (q *RefreshTokenQuerier) GetValid(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	row := q.pool.QueryRow(ctx, `
		SELECT id, user_id, token_hash, issued_at, expires_at, revoked_at
		FROM refresh_tokens
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND expires_at > now()
	`, tokenHash)

	var rt RefreshToken
	if err := row.Scan(
		&rt.ID, &rt.UserID, &rt.TokenHash,
		&rt.IssuedAt, &rt.ExpiresAt, &rt.RevokedAt,
	); err != nil {
		return nil, err
	}
	return &rt, nil
}

func (q *RefreshTokenQuerier) Revoke(ctx context.Context, tokenHash string) error {
	_, err := q.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1`, tokenHash)
	return err
}

func (q *RefreshTokenQuerier) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := q.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}
