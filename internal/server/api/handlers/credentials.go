package handlers

import (
	"net/http"
	"time"

	"github.com/abottVU/netbox-failover/internal/server/crypto"
	"github.com/abottVU/netbox-failover/internal/server/db/queries"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// CredentialHandler handles credential CRUD for clusters.
type CredentialHandler struct {
	creds *queries.CredentialQuerier
	enc   *crypto.Encryptor
}

func NewCredentialHandler(creds *queries.CredentialQuerier, enc *crypto.Encryptor) *CredentialHandler {
	return &CredentialHandler{creds: creds, enc: enc}
}

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

var validCredKinds = map[string]bool{
	"postgres_superuser":     true,
	"postgres_replication":   true,
	"netbox_db_user":         true,
	"redis_password":         true,
	"patroni_rest_password":  true,
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
	if req.Username == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "username is required")
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
