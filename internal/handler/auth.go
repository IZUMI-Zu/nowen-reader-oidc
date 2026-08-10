package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"github.com/nowen-reader/nowen-reader/internal/auth/oidcruntime"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
	"github.com/nowen-reader/nowen-reader/internal/model"
	"github.com/nowen-reader/nowen-reader/internal/store"
)

// AuthHandler handles all auth-related API endpoints.
type AuthHandler struct {
	oidc oidcRuntime
}

type oidcLoginService interface {
	Begin(ctx context.Context, request oidcauth.BeginRequest) (oidcauth.AuthorizationRedirect, error)
	Complete(ctx context.Context, request oidcauth.CallbackRequest) (oidcauth.AuthenticatedIdentity, error)
	Cancel(ctx context.Context, request oidcauth.CancelRequest) (string, error)
}

type oidcRuntime interface {
	oidcLoginService
	State() oidcruntime.State
	WithStateLease(run oidcruntime.StateLease) error
	CompleteAndFinalize(ctx context.Context, request oidcauth.CallbackRequest, finalize oidcruntime.CompletionFinalizer) (oidcauth.AuthenticatedIdentity, error)
	CompleteConfigTest(ctx context.Context, actorUserID, actorSessionID, requestID string, identity oidcauth.AuthenticatedIdentity) (oidcruntime.AdminConfig, error)
	RecordAdminFailure(ctx context.Context, actorUserID, action, requestID, code string) error
}

const passwordLoginDisabledCode = "password_login_disabled"
const minimumPasswordLength = 6

func NewAuthHandler() *AuthHandler {
	runtime, err := NewOIDCRuntime()
	if err != nil {
		log.Printf("[Auth] OIDC runtime initialization failed; local authentication remains available: %v", err)
		return newAuthHandlerWithOIDC(config.OIDCConfig{}, nil, err)
	}
	return newAuthHandlerWithRuntime(runtime)
}

// NewOIDCRuntime resolves exactly one configuration authority and builds the
// process-wide hot-swappable OIDC runtime used by authentication and admin UI.
func NewOIDCRuntime() (*oidcruntime.Manager, error) {
	mode, modeErr := config.GetOIDCConfigMode()
	forcePasswordLogin, forceErr := config.GetOIDCForcePasswordLogin()
	if forceErr != nil {
		log.Printf("[Auth] %v; forcing password login on for recovery", forceErr)
	}

	options := oidcruntime.Options{
		Source:             oidcruntime.ConfigSourceDatabase,
		Repository:         store.OIDCConfigStore{},
		Safety:             store.OIDCConfigStore{},
		Transactions:       store.OIDCTransactionStore{},
		BasePath:           config.BasePath(),
		ForcePasswordLogin: forcePasswordLogin,
	}
	if mode == config.OIDCConfigModeEnvironment || modeErr != nil {
		environmentConfig, environmentErr := config.GetOIDCConfig()
		options.Source = oidcruntime.ConfigSourceEnvironment
		options.EnvironmentConfig = environmentConfig
		options.EnvironmentError = errors.Join(modeErr, environmentErr)
		if options.EnvironmentError != nil {
			log.Printf("[Auth] environment-managed OIDC configuration is invalid: %v", options.EnvironmentError)
		}
	} else {
		protector, protectorErr := oidcruntime.NewFileSecretProtector(config.DataDir(), os.Getenv("OIDC_CONFIG_KEY_FILE"))
		if protectorErr != nil {
			log.Printf("[Auth] Web-managed OIDC client secrets are unavailable: %v", protectorErr)
		} else {
			options.Protector = protector
		}
	}
	return oidcruntime.NewManager(context.Background(), options)
}

// Register handles POST /api/auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	_ = h.oidc.WithStateLease(func(state oidcruntime.State) error {
		h.registerWithState(c, state)
		return nil
	})
}

func (h *AuthHandler) registerWithState(c *gin.Context, state oidcruntime.State) {
	if !passwordLoginEnabled(c, state) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Nickname string `json:"nickname"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username must be 3-32 characters"})
		return
	}
	if len(req.Password) < minimumPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password must be at least 6 characters"})
		return
	}

	// 检查注册策略（第一个用户始终允许注册）
	userCount, err := store.CountUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	if userCount > 0 {
		mode := config.GetRegistrationMode()
		if mode == "closed" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Registration is closed"})
			return
		}
		if mode == "invite" {
			// 仅管理员可以通过管理界面创建用户，普通注册被禁止
			c.JSON(http.StatusForbidden, gin.H{"error": "Registration requires an invitation from admin"})
			return
		}
	} else if oidcBootstrapReady(state) {
		c.JSON(http.StatusForbidden, gin.H{"error": "The first administrator must sign in through the configured OIDC bootstrap identity"})
		return
	}

	// Check if username already exists
	existing, err := store.GetUserByUsername(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	if existing != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username already exists"})
		return
	}

	// Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	// First user is admin
	role := "user"
	if userCount == 0 {
		role = "admin"
	}

	nickname := req.Nickname
	if nickname == "" {
		nickname = req.Username
	}

	user := &model.User{
		ID:        uuid.New().String(),
		Username:  req.Username,
		Password:  string(hashedPassword),
		Nickname:  nickname,
		Role:      role,
		AiEnabled: role == "admin", // 管理员默认启用 AI
	}

	if err := store.CreateUser(user); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Registration failed"})
		return
	}

	// Auto-login after registration
	if err := h.issueSession(c, user.ID, model.SessionAuthMethodPassword, 0, false); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user": model.AuthUser{
			ID:          user.ID,
			Username:    user.Username,
			Nickname:    user.Nickname,
			Role:        user.Role,
			AiEnabled:   user.AiEnabled,
			HasPassword: true,
		},
	})
}

// Login handles POST /api/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	_ = h.oidc.WithStateLease(func(state oidcruntime.State) error {
		h.loginWithState(c, state)
		return nil
	})
}

func (h *AuthHandler) loginWithState(c *gin.Context, state oidcruntime.State) {
	if !passwordLoginEnabled(c, state) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	user, err := store.GetUserByUsername(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	if user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}
	if user.Password == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
		return
	}

	// Create session
	if err := h.issueSession(c, user.ID, model.SessionAuthMethodPassword, 0, false); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user": model.AuthUser{
			ID:          user.ID,
			Username:    user.Username,
			Nickname:    user.Nickname,
			Role:        user.Role,
			AiEnabled:   user.AiEnabled,
			HasPassword: true,
		},
	})
}

func passwordLoginEnabled(c *gin.Context, state oidcruntime.State) bool {
	if !state.Config.DisablePasswordLogin {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{
		"error": "Password login is disabled; use OpenID Connect",
		"code":  passwordLoginDisabledCode,
	})
	return false
}

// PasswordReauth refreshes the current browser session's authentication time
// after verifying the user's real local password. It is used before binding
// external identities and other credential-changing operations.
func (h *AuthHandler) PasswordReauth(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
		return
	}
	currentUser := middleware.GetCurrentUser(c)
	credential := middleware.GetCurrentCredential(c)
	if currentUser == nil || credential == nil || credential.Type != middleware.CredentialSession {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Browser session required"})
		return
	}
	user, err := store.GetUserByID(currentUser.ID)
	if err != nil || user == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}
	if user.Password == "" || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Current password is incorrect"})
		return
	}
	if err := store.MarkSessionAuthenticated(credential.ID, time.Now().UTC()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to refresh authentication"})
		return
	}
	c.Status(http.StatusNoContent)
}

// SetInitialPassword adds a local break-glass credential to an OIDC-only
// account. The route requires a recent browser authentication and cannot be
// used to replace an existing password.
func (h *AuthHandler) SetInitialPassword(c *gin.Context) {
	var req struct {
		NewPassword string `json:"newPassword"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.NewPassword) < minimumPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "New password must be at least 6 characters"})
		return
	}
	currentUser := middleware.GetCurrentUser(c)
	credential := middleware.GetCurrentCredential(c)
	if currentUser == nil || credential == nil || credential.Type != middleware.CredentialSession {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Browser session required"})
		return
	}
	user, err := store.GetUserByID(currentUser.ID)
	if err != nil || user == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}
	if user.Password != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "A local password is already configured"})
		return
	}
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}
	created, err := store.SetInitialUserPassword(user.ID, string(hashedPassword))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set password"})
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{"error": "A local password is already configured"})
		return
	}
	if err := store.DeleteSessionsByUserID(user.ID, credential.ID); err != nil {
		log.Printf("[auth] Warning: failed to cleanup sessions after initial password setup for user %s: %v", user.ID, err)
	}
	c.Status(http.StatusNoContent)
}

// Logout handles POST /api/auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	token, err := c.Cookie(middleware.SessionCookie)
	if err == nil && token != "" {
		_ = store.DeleteSession(token)
	}

	middleware.ClearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// Me handles GET /api/auth/me
func (h *AuthHandler) Me(c *gin.Context) {
	hasUsers, err := store.CountUsers()
	if err != nil {
		// 数据库出错时返回500，避免前端误以为未登录而跳转到登录页
		log.Printf("[Auth] CountUsers error in /api/auth/me: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}

	state := h.oidc.State()
	if hasUsers == 0 {
		oidcConfig := state.Config
		oidcStatus := oidcStatus(state)
		bootstrapReady := oidcBootstrapReady(state)
		passwordLoginEnabled := !oidcConfig.DisablePasswordLogin
		registrationMode := "closed"
		if passwordLoginEnabled && !bootstrapReady {
			registrationMode = "open"
		}
		c.JSON(http.StatusOK, gin.H{
			"user": nil, "needsSetup": passwordLoginEnabled && !bootstrapReady, "registrationMode": registrationMode,
			"loginMethods": gin.H{"password": passwordLoginEnabled, "oidc": oidcStatus},
		})
		return
	}

	oidcConfig := state.Config
	user := middleware.GetCurrentUser(c)
	if c.GetHeader("Authorization") != "" && user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	oidcStatus := oidcStatus(state)
	if user != nil && oidcConfig.Enabled {
		if linked, err := store.HasOIDCIdentityForUser(c.Request.Context(), user.ID, oidcConfig.IssuerURL); err == nil {
			oidcStatus["linked"] = linked
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"user":             user,
		"needsSetup":       false,
		"registrationMode": passwordRegistrationMode(oidcConfig.DisablePasswordLogin),
		"loginMethods":     gin.H{"password": !oidcConfig.DisablePasswordLogin, "oidc": oidcStatus},
	})
}

func passwordRegistrationMode(disabled bool) string {
	if disabled {
		return "closed"
	}
	return config.GetRegistrationMode()
}

// ListUsers handles GET /api/auth/users (admin only)
func (h *AuthHandler) ListUsers(c *gin.Context) {
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil || currentUser.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
		return
	}

	users, err := store.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list users"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"users": users})
}

// UpdateUser handles PUT /api/auth/users
func (h *AuthHandler) UpdateUser(c *gin.Context) {
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req struct {
		Action      string `json:"action"`
		UserID      string `json:"userId"`
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
		Nickname    string `json:"nickname"`
		Role        string `json:"role"`
		AiEnabled   bool   `json:"aiEnabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	switch req.Action {
	case "changePassword":
		if len(req.NewPassword) < minimumPasswordLength {
			c.JSON(http.StatusBadRequest, gin.H{"error": "New password must be at least 6 characters"})
			return
		}
		targetID := req.UserID
		if targetID == "" {
			targetID = currentUser.ID
		}
		// Non-admin can only change own password
		if targetID != currentUser.ID && currentUser.Role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
			return
		}

		user, err := store.GetUserByID(targetID)
		if err != nil || user == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "User not found"})
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.OldPassword)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Current password is incorrect"})
			return
		}

		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
			return
		}

		if err := store.UpdateUserPassword(targetID, string(hashedPassword)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update password"})
			return
		}

		// Invalidate all other sessions for this user (security best practice)
		currentToken, _ := c.Cookie(middleware.SessionCookie)
		if err := store.DeleteSessionsByUserID(targetID, currentToken); err != nil {
			// Non-fatal: log but don't fail the request
			log.Printf("[auth] Warning: failed to cleanup sessions after password change for user %s: %v", targetID, err)
		}

		c.JSON(http.StatusOK, gin.H{"success": true})

	case "updateProfile":
		targetID := req.UserID
		if targetID == "" {
			targetID = currentUser.ID
		}
		if targetID != currentUser.ID && currentUser.Role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
			return
		}

		if err := store.UpdateUserProfile(targetID, req.Nickname); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"success": true})

	case "updateRole":
		if currentUser.Role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
			return
		}
		if req.UserID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "userId is required"})
			return
		}
		if req.UserID == currentUser.ID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot change your own role"})
			return
		}
		newRole := req.Role
		if newRole != "admin" && newRole != "user" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role, must be 'admin' or 'user'"})
			return
		}
		err := h.oidc.WithStateLease(func(state oidcruntime.State) error {
			return store.UpdateUserRolePreservingOIDCAdmin(req.UserID, newRole, protectedOIDCAdminIssuer(state))
		})
		if errors.Is(err, store.ErrOIDCLastBoundAdministrator) {
			c.JSON(http.StatusConflict, gin.H{
				"error": "At least one administrator must remain linked to the active OIDC provider",
				"code":  "oidc_admin_required",
			})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update role"})
			return
		}
		// 管理员自动启用 AI
		if newRole == "admin" {
			_ = store.UpdateUserAiEnabled(req.UserID, true)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})

	case "updateAiEnabled":
		if currentUser.Role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
			return
		}
		if req.UserID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "userId is required"})
			return
		}
		if err := store.UpdateUserAiEnabled(req.UserID, req.AiEnabled); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update AI access"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid action"})
	}
}

// DeleteUserHandler handles DELETE /api/auth/users (admin only)
func (h *AuthHandler) DeleteUserHandler(c *gin.Context) {
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil || currentUser.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
		return
	}

	var req struct {
		UserID string `json:"userId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.UserID == currentUser.ID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot delete yourself"})
		return
	}

	err := h.oidc.WithStateLease(func(state oidcruntime.State) error {
		return store.DeleteUserPreservingOIDCAdmin(req.UserID, protectedOIDCAdminIssuer(state))
	})
	if errors.Is(err, store.ErrOIDCLastBoundAdministrator) {
		c.JSON(http.StatusConflict, gin.H{
			"error": "At least one administrator must remain linked to the active OIDC provider",
			"code":  "oidc_admin_required",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func protectedOIDCAdminIssuer(state oidcruntime.State) string {
	if state.Config.DisablePasswordLogin {
		return state.Config.IssuerURL
	}
	return ""
}

// CreateUserByAdmin handles POST /api/auth/users (admin only)
// 管理员直接创建用户（用于邀请模式或关闭注册模式下添加用户）
func (h *AuthHandler) CreateUserByAdmin(c *gin.Context) {
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil || currentUser.Role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Unauthorized"})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Nickname string `json:"nickname"`
		Role     string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}
	if len(req.Username) < 3 || len(req.Username) > 32 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username must be 3-32 characters"})
		return
	}
	if len(req.Password) < minimumPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password must be at least 6 characters"})
		return
	}

	existing, err := store.GetUserByUsername(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		return
	}
	if existing != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username already exists"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	role := "user"
	if req.Role == "admin" || req.Role == "user" {
		role = req.Role
	}

	nickname := req.Nickname
	if nickname == "" {
		nickname = req.Username
	}

	user := &model.User{
		ID:        uuid.New().String(),
		Username:  req.Username,
		Password:  string(hashedPassword),
		Nickname:  nickname,
		Role:      role,
		AiEnabled: role == "admin",
	}

	if err := store.CreateUser(user); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to create user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"user": model.AuthUser{
			ID:          user.ID,
			Username:    user.Username,
			Nickname:    user.Nickname,
			Role:        user.Role,
			AiEnabled:   user.AiEnabled,
			HasPassword: true,
		},
	})
}
