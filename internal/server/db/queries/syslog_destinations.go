package queries

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SyslogDestination represents one syslog forwarding target.
type SyslogDestination struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Protocol    string    `json:"protocol"` // udp | tcp | tcp+tls
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	TLSCACert   *string   `json:"tls_ca_cert,omitempty"`
	Categories  []string  `json:"categories"` // empty = all
	MinSeverity string    `json:"min_severity"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SyslogDestinationParams holds the fields for create/update.
type SyslogDestinationParams struct {
	Name        string
	Protocol    string
	Host        string
	Port        int
	TLSCACert   *string
	Categories  []string
	MinSeverity string
	Enabled     bool
}

// SyslogDestinationQuerier performs syslog_destinations operations.
type SyslogDestinationQuerier struct {
	pool *pgxpool.Pool
}

func NewSyslogDestinationQuerier(pool *pgxpool.Pool) *SyslogDestinationQuerier {
	return &SyslogDestinationQuerier{pool: pool}
}

func (q *SyslogDestinationQuerier) Create(ctx context.Context, p SyslogDestinationParams) (*SyslogDestination, error) {
	var d SyslogDestination
	row := q.pool.QueryRow(ctx, `
		INSERT INTO syslog_destinations (name, protocol, host, port, tls_ca_cert, categories, min_severity, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, name, protocol, host, port, tls_ca_cert, categories, min_severity, enabled, created_at, updated_at`,
		p.Name, p.Protocol, p.Host, p.Port, p.TLSCACert, p.Categories, p.MinSeverity, p.Enabled,
	)
	return &d, scanSyslogDest(row, &d)
}

func (q *SyslogDestinationQuerier) List(ctx context.Context) ([]SyslogDestination, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, protocol, host, port, tls_ca_cert, categories, min_severity, enabled, created_at, updated_at
		FROM syslog_destinations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SyslogDestination
	for rows.Next() {
		var d SyslogDestination
		if err := scanSyslogDest(rows, &d); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (q *SyslogDestinationQuerier) ListEnabled(ctx context.Context) ([]SyslogDestination, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, name, protocol, host, port, tls_ca_cert, categories, min_severity, enabled, created_at, updated_at
		FROM syslog_destinations WHERE enabled = TRUE ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SyslogDestination
	for rows.Next() {
		var d SyslogDestination
		if err := scanSyslogDest(rows, &d); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (q *SyslogDestinationQuerier) GetByID(ctx context.Context, id uuid.UUID) (*SyslogDestination, error) {
	var d SyslogDestination
	row := q.pool.QueryRow(ctx, `
		SELECT id, name, protocol, host, port, tls_ca_cert, categories, min_severity, enabled, created_at, updated_at
		FROM syslog_destinations WHERE id = $1`, id)
	return &d, scanSyslogDest(row, &d)
}

func (q *SyslogDestinationQuerier) Update(ctx context.Context, id uuid.UUID, p SyslogDestinationParams) (*SyslogDestination, error) {
	var d SyslogDestination
	row := q.pool.QueryRow(ctx, `
		UPDATE syslog_destinations
		SET name = $2, protocol = $3, host = $4, port = $5, tls_ca_cert = $6,
		    categories = $7, min_severity = $8, enabled = $9, updated_at = now()
		WHERE id = $1
		RETURNING id, name, protocol, host, port, tls_ca_cert, categories, min_severity, enabled, created_at, updated_at`,
		id, p.Name, p.Protocol, p.Host, p.Port, p.TLSCACert,
		p.Categories, p.MinSeverity, p.Enabled,
	)
	return &d, scanSyslogDest(row, &d)
}

func (q *SyslogDestinationQuerier) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := q.pool.Exec(ctx, `DELETE FROM syslog_destinations WHERE id = $1`, id)
	return err
}

func scanSyslogDest(row rowScanner, d *SyslogDestination) error {
	return row.Scan(
		&d.ID, &d.Name, &d.Protocol, &d.Host, &d.Port, &d.TLSCACert,
		&d.Categories, &d.MinSeverity, &d.Enabled, &d.CreatedAt, &d.UpdatedAt,
	)
}
