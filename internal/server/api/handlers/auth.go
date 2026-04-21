package handlers

import (
	"net/http"
	"time"

	"github.com/averyhabbott/netbox-conductor/internal/server/api/jwtutil"
	"github.com/averyhabbott/netbox-conductor/internal/server/api/middleware"
	"github.com/averyhabbott/netbox-conductor/internal/server/crypto"
	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/averyhabbott/netbox-conductor/internal/server/tlscert"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	users         *queries.UserQuerier
	refreshTokens *queries.RefreshTokenQuerier
	jwtSecret     []byte
	certFile      string            // path to TLS cert, empty if TLS disabled
	keyFile       string            // path to TLS private key
	serverURL     string            // SERVER_URL env value (used for cert SAN generation)
	enc           *crypto.Encryptor // for encrypting TOTP secrets at rest
}

func NewAuthHandler(
	users *queries.UserQuerier,
	refreshTokens *queries.RefreshTokenQuerier,
	jwtSecret []byte,
	certFile string,
	keyFile string,
	serverURL string,
	enc *crypto.Encryptor,
) *AuthHandler {
	return &AuthHandler{
		users:         users,
		refreshTokens: refreshTokens,
		jwtSecret:     jwtSecret,
		certFile:      certFile,
		keyFile:       keyFile,
		serverURL:     serverURL,
		enc:           enc,
	}
}

type loginRequest struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds
}

// Login godoc
// POST /api/v1/auth/login
func (h *AuthHandler) Login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	user, err := h.users.GetByUsername(c.Request().Context(), req.Username)
	if err != nil {
		// Don't reveal whether the user exists
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}

	// If TOTP is enabled, issue a short-lived pending token instead of full tokens.
	if user.TOTPEnabled {
		pendingToken, err := jwtutil.IssueTOTPPending(user.ID, h.jwtSecret)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "could not issue totp token")
		}
		return c.JSON(http.StatusOK, map[string]any{
			"requires_totp": true,
			"totp_token":    pendingToken,
		})
	}

	return h.issueFullTokens(c, user)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// Refresh godoc
// POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	hash := crypto.HashToken(req.RefreshToken)
	rt, err := h.refreshTokens.GetValid(c.Request().Context(), hash)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired refresh token")
	}

	user, err := h.users.GetByID(c.Request().Context(), rt.UserID)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "user not found")
	}

	accessToken, err := jwtutil.IssueAccess(user.ID, user.Username, user.Role, h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not issue token")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"access_token": accessToken,
		"expires_in":   int(jwtutil.AccessTokenTTL.Seconds()),
	})
}

// Logout godoc
// POST /api/v1/auth/logout
func (h *AuthHandler) Logout(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err == nil && req.RefreshToken != "" {
		hash := crypto.HashToken(req.RefreshToken)
		_ = h.refreshTokens.Revoke(c.Request().Context(), hash)
	}
	return c.NoContent(http.StatusNoContent)
}

type meResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// Me godoc
// GET /api/v1/auth/me
func (h *AuthHandler) Me(c echo.Context) error {
	userIDStr, _ := c.Get(middleware.ContextKeyUserID).(string)
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid user context")
	}

	user, err := h.users.GetByID(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}

	return c.JSON(http.StatusOK, meResponse{
		ID:       user.ID.String(),
		Username: user.Username,
		Role:     user.Role,
	})
}

// userListItem is the public representation of a user (no password hash).
type userListItem struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"created_at"`
	LastLoginAt *string `json:"last_login_at"`
}

func toUserListItem(u queries.User) userListItem {
	item := userListItem{
		ID:        u.ID.String(),
		Username:  u.Username,
		Role:      u.Role,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
	}
	if u.LastLoginAt != nil {
		s := u.LastLoginAt.Format(time.RFC3339)
		item.LastLoginAt = &s
	}
	return item
}

// ListUsers godoc
// GET /api/v1/users
func (h *AuthHandler) ListUsers(c echo.Context) error {
	users, err := h.users.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list users")
	}
	out := make([]userListItem, len(users))
	for i, u := range users {
		out[i] = toUserListItem(u)
	}
	return c.JSON(http.StatusOK, out)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// CreateUser godoc
// POST /api/v1/users
func (h *AuthHandler) CreateUser(c echo.Context) error {
	var req createUserRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if req.Username == "" || req.Password == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "username and password are required")
	}
	validRoles := map[string]bool{"admin": true, "operator": true, "viewer": true}
	if !validRoles[req.Role] {
		return echo.NewHTTPError(http.StatusBadRequest, "role must be admin, operator, or viewer")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to hash password")
	}
	user, err := h.users.Create(c.Request().Context(), req.Username, string(hash), req.Role)
	if err != nil {
		return echo.NewHTTPError(http.StatusConflict, "username already exists")
	}
	return c.JSON(http.StatusCreated, toUserListItem(*user))
}

type updateRoleRequest struct {
	Role string `json:"role"`
}

// UpdateUserRole godoc
// PATCH /api/v1/users/:id/role
func (h *AuthHandler) UpdateUserRole(c echo.Context) error {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid user id")
	}
	var req updateRoleRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	validRoles := map[string]bool{"admin": true, "operator": true, "viewer": true}
	if !validRoles[req.Role] {
		return echo.NewHTTPError(http.StatusBadRequest, "role must be admin, operator, or viewer")
	}
	if err := h.users.UpdateRole(c.Request().Context(), targetID, req.Role); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update role")
	}
	return c.NoContent(http.StatusNoContent)
}

// DeleteUser godoc
// DELETE /api/v1/users/:id
func (h *AuthHandler) DeleteUser(c echo.Context) error {
	callerID, _ := c.Get(middleware.ContextKeyUserID).(string)
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid user id")
	}
	if callerID == targetID.String() {
		return echo.NewHTTPError(http.StatusBadRequest, "cannot delete your own account")
	}
	if err := h.users.Delete(c.Request().Context(), targetID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete user")
	}
	return c.NoContent(http.StatusNoContent)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword godoc
// POST /api/v1/auth/change-password
func (h *AuthHandler) ChangePassword(c echo.Context) error {
	callerIDStr, _ := c.Get(middleware.ContextKeyUserID).(string)
	callerID, err := uuid.Parse(callerIDStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid user context")
	}
	var req changePasswordRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if len(req.NewPassword) < 8 {
		return echo.NewHTTPError(http.StatusBadRequest, "new password must be at least 8 characters")
	}
	user, err := h.users.GetByID(c.Request().Context(), callerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "current password is incorrect")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to hash password")
	}
	if err := h.users.UpdatePassword(c.Request().Context(), callerID, string(hash)); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update password")
	}
	// Revoke all refresh tokens so existing sessions are invalidated
	_ = h.refreshTokens.RevokeAllForUser(c.Request().Context(), callerID)
	return c.NoContent(http.StatusNoContent)
}

type tlsInfoResponse struct {
	Enabled     bool              `json:"enabled"`
	CertInfo    *tlscert.CertInfo `json:"cert_info,omitempty"`
}

// TLSInfo godoc
// GET /api/v1/settings/tls
func (h *AuthHandler) TLSInfo(c echo.Context) error {
	if h.certFile == "" {
		return c.JSON(http.StatusOK, tlsInfoResponse{Enabled: false})
	}
	info, err := tlscert.ReadCertInfo(h.certFile)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read cert info")
	}
	return c.JSON(http.StatusOK, tlsInfoResponse{
		Enabled:  info != nil,
		CertInfo: info,
	})
}

// RegenerateCert godoc
// POST /api/v1/settings/tls/regenerate
func (h *AuthHandler) RegenerateCert(c echo.Context) error {
	if h.certFile == "" || h.keyFile == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "TLS is not enabled on this server")
	}

	var req struct {
		ServerURL string `json:"server_url"`
	}
	_ = c.Bind(&req)

	serverURL := h.serverURL
	if req.ServerURL != "" {
		serverURL = req.ServerURL
	}

	dnsNames, ipAddrs := tlscert.SANsFromServerURL(serverURL)
	if err := tlscert.Regenerate(h.certFile, h.keyFile, dnsNames, ipAddrs); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to regenerate cert: "+err.Error())
	}

	info, err := tlscert.ReadCertInfo(h.certFile)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read new cert info")
	}
	return c.JSON(http.StatusOK, tlsInfoResponse{Enabled: true, CertInfo: info})
}

// ─── TOTP ──────────────────────────────────────────────────────────────────────

// VerifyTOTP completes a two-step login when TOTP is enabled.
// POST /api/v1/auth/totp/verify  (no JWT — totp_token acts as auth)
func (h *AuthHandler) VerifyTOTP(c echo.Context) error {
	var req struct {
		TOTPToken string `json:"totp_token"`
		Code      string `json:"code"`
	}
	if err := c.Bind(&req); err != nil || req.TOTPToken == "" || req.Code == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "totp_token and code are required")
	}

	userID, err := jwtutil.ParseTOTPPending(req.TOTPToken, h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired totp_token")
	}

	user, err := h.users.GetByID(c.Request().Context(), userID)
	if err != nil || !user.TOTPEnabled || len(user.TOTPSecretEnc) == 0 {
		return echo.NewHTTPError(http.StatusUnauthorized, "TOTP not configured for user")
	}

	secretBytes, err := h.enc.Decrypt(user.TOTPSecretEnc)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read TOTP secret")
	}

	if !totp.Validate(req.Code, string(secretBytes)) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid TOTP code")
	}

	return h.issueFullTokens(c, user)
}

// EnrollTOTP begins TOTP enrollment for the authenticated user.
// Returns a QR URI and an enrollment_token the client must send back to confirm.
// POST /api/v1/auth/totp/enroll
func (h *AuthHandler) EnrollTOTP(c echo.Context) error {
	userID := h.callerID(c)
	user, err := h.users.GetByID(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}

	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "NetBox Conductor",
		AccountName: user.Username,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate TOTP secret")
	}

	enrollToken, err := jwtutil.IssueTOTPEnroll(userID, key.Secret(), h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to issue enrollment token")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"qr_uri":          key.URL(),
		"secret":          key.Secret(), // shown once for manual entry
		"enrollment_token": enrollToken,
	})
}

// ConfirmTOTP verifies the TOTP code and saves the secret, enabling TOTP.
// POST /api/v1/auth/totp/confirm
func (h *AuthHandler) ConfirmTOTP(c echo.Context) error {
	var req struct {
		EnrollmentToken string `json:"enrollment_token"`
		Code            string `json:"code"`
	}
	if err := c.Bind(&req); err != nil || req.EnrollmentToken == "" || req.Code == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "enrollment_token and code are required")
	}

	callerID := h.callerID(c)
	enrollUserID, secret, err := jwtutil.ParseTOTPEnroll(req.EnrollmentToken, h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid or expired enrollment_token")
	}
	if enrollUserID != callerID {
		return echo.NewHTTPError(http.StatusForbidden, "enrollment token belongs to a different user")
	}

	if !totp.Validate(req.Code, secret) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid TOTP code — check your authenticator app and try again")
	}

	secretEnc, err := h.enc.Encrypt([]byte(secret))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to encrypt TOTP secret")
	}

	if err := h.users.SetTOTP(c.Request().Context(), callerID, secretEnc); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save TOTP secret")
	}

	return c.JSON(http.StatusOK, map[string]any{"totp_enabled": true})
}

// DisableTOTP removes TOTP from the authenticated user's account.
// POST /api/v1/auth/totp/disable
func (h *AuthHandler) DisableTOTP(c echo.Context) error {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "password is required")
	}

	callerID := h.callerID(c)
	user, err := h.users.GetByID(c.Request().Context(), callerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "incorrect password")
	}

	if err := h.users.ClearTOTP(c.Request().Context(), callerID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to disable TOTP")
	}

	return c.JSON(http.StatusOK, map[string]any{"totp_enabled": false})
}

// TOTPStatus reports whether TOTP is enabled for the authenticated user.
// GET /api/v1/auth/totp/status
func (h *AuthHandler) TOTPStatus(c echo.Context) error {
	callerID := h.callerID(c)
	user, err := h.users.GetByID(c.Request().Context(), callerID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"totp_enabled": user.TOTPEnabled})
}

// callerID extracts the authenticated user's UUID from the request context.
func (h *AuthHandler) callerID(c echo.Context) uuid.UUID {
	s, _ := c.Get(middleware.ContextKeyUserID).(string)
	id, _ := uuid.Parse(s)
	return id
}

// issueFullTokens is shared by Login (no TOTP) and VerifyTOTP.
func (h *AuthHandler) issueFullTokens(c echo.Context, user *queries.User) error {
	accessToken, err := jwtutil.IssueAccess(user.ID, user.Username, user.Role, h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not issue token")
	}
	rawRefresh, err := jwtutil.GenerateRefreshToken()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not generate refresh token")
	}
	expiresAt := time.Now().Add(jwtutil.RefreshTokenTTL)
	if err := h.refreshTokens.Create(c.Request().Context(), user.ID, crypto.HashToken(rawRefresh), expiresAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not store refresh token")
	}
	_ = h.users.UpdateLastLogin(c.Request().Context(), user.ID)
	return c.JSON(http.StatusOK, tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		ExpiresIn:    int(jwtutil.AccessTokenTTL.Seconds()),
	})
}
