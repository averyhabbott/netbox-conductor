package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/events"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// CredentialHandler handles credential CRUD for clusters.
type CredentialHandler struct {
	creds   *queries.CredentialQuerier
	enc     *crypto.Encryptor
	emitter events.Emitter
}

func NewCredentialHandler(creds *queries.CredentialQuerier, enc *crypto.Encryptor) *CredentialHandler {
	return &CredentialHandler{creds: creds, enc: enc}
}

func (h *CredentialHandler) SetEmitter(e events.Emitter) { h.emitter = e }

type credentialResponse struct {
	ID        string  `json:"id"`
	ClusterID string  `json:"cluster_id"`
	Kind      string  `json:"kind"`
	Username  string  `json:"username"`
	DBName    *string `json:"db_name,omitempty"`
	CreatedAt string  `json:"created_at"`
	RotatedAt *string `json:"rotated_at,omitempty"`
}

func toCredentialResponse(c *queries.Credential) credentialResponse {
	r := credentialResponse{
		ID:        c.ID.String(),
		ClusterID: c.ClusterID.String(),
		Kind:      c.Kind,
		Username:  c.Username,
		DBName:    c.DBName,
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
	}
	if c.RotatedAt != nil {
		s := c.RotatedAt.Format(time.RFC3339)
		r.RotatedAt = &s
	}
	return r
}

// List godoc
// GET /api/v1/clusters/:id/credentials
func (h *CredentialHandler) List(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	creds, err := h.creds.ListByCluster(c.Request().Context(), clusterID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list credentials")
	}

	resp := make([]credentialResponse, 0, len(creds))
	for i := range creds {
		resp = append(resp, toCredentialResponse(&creds[i]))
	}
	return c.JSON(http.StatusOK, resp)
}

type upsertCredentialRequest struct {
	Username string  `json:"username"`
	Password string  `json:"password"`
	DBName   *string `json:"db_name"`
}

// Credential kind constants. Used in DB lookups, JSON payloads, and as the
// authoritative source for which credentials a cluster may store. Code paths
// that build Patroni configs or look up specific credentials should use these
// rather than string literals so a rename propagates cleanly.
const (
	CredKindPostgresSuperuser   = "postgres_superuser"
	CredKindPostgresReplication = "postgres_replication"
	CredKindNetboxDBUser        = "netbox_db_user"
	CredKindRedisTasks          = "redis_tasks_password"
	CredKindRedisCaching        = "redis_caching_password"
	CredKindNetboxSecretKey     = "netbox_secret_key"
	CredKindNetboxAPITokenPepper = "netbox_api_token_pepper"
	CredKindPatroniREST         = "patroni_rest_password"
)

// Default usernames used when a credential record is created or when a config
// builder needs a sensible fallback. Single source of truth — the credential
// generator and the fallbacks in PushPatroniConfig must agree, otherwise a
// fresh cluster could generate "replicator" while the config generator wrote
// "replica" into patroni.yml.
const (
	CredDefaultUserPostgresSuperuser   = "postgres"
	CredDefaultUserPostgresReplication = "replicator"
	CredDefaultUserPatroniREST         = "patroni"
	CredDefaultUserNetboxDB            = "netbox"
	CredDefaultDBNetbox                = "netbox"
)

var validCredKinds = map[string]bool{
	CredKindPostgresSuperuser:    true,
	CredKindPostgresReplication:  true,
	CredKindNetboxDBUser:         true,
	CredKindRedisTasks:           true,
	CredKindRedisCaching:         true,
	CredKindNetboxSecretKey:      true,
	CredKindNetboxAPITokenPepper: true,
	CredKindPatroniREST:          true,
}

// autoGenDefaults maps credential kind → default username (and optional db name).
var autoGenDefaults = []struct {
	Kind     string
	Username string
	DBName   *string
}{
	{Kind: CredKindPostgresSuperuser, Username: CredDefaultUserPostgresSuperuser},
	{Kind: CredKindPostgresReplication, Username: CredDefaultUserPostgresReplication},
	{Kind: CredKindNetboxDBUser, Username: CredDefaultUserNetboxDB, DBName: strPtr(CredDefaultDBNetbox)},
	{Kind: CredKindRedisTasks, Username: ""},
	{Kind: CredKindPatroniREST, Username: CredDefaultUserPatroniREST},
}

func strPtr(s string) *string { return &s }

// GenerateCredentials auto-generates secure random passwords for all
// non-superuser credential kinds. Returns the plaintext values once.
// POST /api/v1/clusters/:id/credentials/generate?missing_only=true
func (h *CredentialHandler) GenerateCredentials(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	missingOnly := c.QueryParam("missing_only") == "true"

	var existing map[string]bool
	if missingOnly {
		existing = make(map[string]bool)
		creds, err := h.creds.ListByCluster(c.Request().Context(), clusterID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to list credentials")
		}
		for _, cr := range creds {
			existing[cr.Kind] = true
		}
	}

	type generated struct {
		Kind     string  `json:"kind"`
		Username string  `json:"username"`
		Password string  `json:"password"`
		DBName   *string `json:"db_name,omitempty"`
	}
	results := make([]generated, 0, len(autoGenDefaults))

	for _, def := range autoGenDefaults {
		if missingOnly && existing[def.Kind] {
			continue
		}
		rawPassword, err := crypto.GenerateToken(32)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate password")
		}
		encPassword, err := h.enc.Encrypt([]byte(rawPassword))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt password")
		}
		if err := h.creds.Upsert(c.Request().Context(), queries.UpsertCredentialParams{
			ClusterID:   clusterID,
			Kind:        def.Kind,
			Username:    def.Username,
			PasswordEnc: encPassword,
			DBName:      def.DBName,
		}); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save credential")
		}
		results = append(results, generated{
			Kind:     def.Kind,
			Username: def.Username,
			Password: rawPassword,
			DBName:   def.DBName,
		})
	}

	if h.emitter != nil && len(results) > 0 {
		cid, _ := uuid.Parse(c.Param("id"))
		h.emitter.Emit(events.New(events.CategoryConfig, events.SeverityWarn, events.CodeCredentialRotated,
			fmt.Sprintf("Credentials generated for cluster (%d kinds)", len(results)), actorFromCtx(c)).
			Cluster(cid).Build())
	}
	return c.JSON(http.StatusOK, map[string]any{
		"generated": results,
		"warning":   "These passwords will not be shown again. Copy them now.",
	})
}

// Upsert godoc
// PUT /api/v1/clusters/:id/credentials/:kind
func (h *CredentialHandler) Upsert(c echo.Context) error {
	clusterID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid cluster id")
	}

	kind := c.Param("kind")
	if !validCredKinds[kind] {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid credential kind")
	}

	var req upsertCredentialRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Password == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "password is required")
	}

	encPassword, err := h.enc.Encrypt([]byte(req.Password))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt password")
	}

	if err := h.creds.Upsert(c.Request().Context(), queries.UpsertCredentialParams{
		ClusterID:   clusterID,
		Kind:        kind,
		Username:    req.Username,
		PasswordEnc: encPassword,
		DBName:      req.DBName,
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save credential")
	}

	cred, err := h.creds.GetByKind(c.Request().Context(), clusterID, kind)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fetch saved credential")
	}

	return c.JSON(http.StatusOK, toCredentialResponse(cred))
}
