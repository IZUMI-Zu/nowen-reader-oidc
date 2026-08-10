package handler

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/nowen-reader/nowen-reader/internal/config"
	"github.com/nowen-reader/nowen-reader/internal/middleware"
)

func registerAuthRoutes(api *gin.RouterGroup) {
	// Auth routes (Phase 1)
	// ============================================================
	oidcRuntime, oidcRuntimeErr := NewOIDCRuntime()
	var auth *AuthHandler
	if oidcRuntimeErr != nil {
		log.Printf("[Auth] OIDC runtime initialization failed; local authentication remains available: %v", oidcRuntimeErr)
		auth = newAuthHandlerWithOIDC(config.OIDCConfig{}, nil, oidcRuntimeErr)
	} else {
		auth = newAuthHandlerWithRuntime(oidcRuntime)
	}
	oidcAdmin := NewOIDCAdminHandler(oidcRuntime, auth)
	apiKeys := NewAPIKeyHandler()

	authGroup := api.Group("/auth")
	{
		// Login/register use strict rate limiting to prevent brute-force
		authGroup.POST("/register", middleware.RateLimitAuth(), auth.Register)
		authGroup.POST("/login", middleware.RateLimitAuth(), auth.Login)
		authGroup.POST("/reauth/password", middleware.SessionRequired(), middleware.RateLimitAuth(), auth.PasswordReauth)
		authGroup.POST("/password", middleware.SessionRequired(), middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), middleware.RateLimitAuth(), auth.SetInitialPassword)
		authGroup.GET("/oidc/login", middleware.RateLimitAuth(), auth.OIDCLogin)
		authGroup.GET("/oidc/callback", middleware.RateLimitAuth(), auth.OIDCCallback)
		authGroup.POST("/oidc/link", middleware.SessionRequired(), middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), middleware.RateLimitAuth(), auth.OIDCLink)
		authGroup.GET("/oidc/reauth", middleware.SessionRequired(), middleware.RateLimitAuth(), auth.OIDCReauth)
		authGroup.DELETE("/oidc/link", middleware.SessionRequired(), middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), middleware.RateLimitAuth(), auth.OIDCUnlink)
		// Logout and session check don't need strict limiting
		authGroup.POST("/logout", auth.Logout)
		authGroup.GET("/me", auth.Me)
	}

	usersGroup := api.Group("/auth/users")
	usersGroup.Use(middleware.AdminRequired())
	{
		usersGroup.GET("", auth.ListUsers)
		usersGroup.POST("", auth.CreateUserByAdmin)
		usersGroup.PUT("", auth.UpdateUser)
		usersGroup.DELETE("", auth.DeleteUserHandler)
	}

	apiKeyGroup := api.Group("/auth/api-keys")
	apiKeyGroup.Use(middleware.SessionRequired())
	{
		apiKeyGroup.GET("", apiKeys.List)
		apiKeyGroup.POST("", middleware.RateLimitAuth(), apiKeys.Create)
		apiKeyGroup.DELETE("/:id", apiKeys.Revoke)
		apiKeyGroup.DELETE("", middleware.RateLimitAuth(), apiKeys.RevokeAll)
	}

	adminAPIKeyGroup := api.Group("/admin/users/:id/api-keys")
	adminAPIKeyGroup.Use(middleware.SessionRequired(), middleware.AdminRequired())
	{
		adminAPIKeyGroup.GET("", apiKeys.AdminList)
		adminAPIKeyGroup.DELETE("", apiKeys.AdminRevokeAll)
	}

	oidcAdminGroup := api.Group("/admin/oidc")
	oidcAdminGroup.Use(middleware.SessionRequired(), middleware.AdminRequired())
	{
		oidcAdminGroup.GET("", oidcAdmin.Get)
		oidcAdminGroup.PUT("", middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), middleware.RateLimitAuth(), oidcAdmin.Update)
		oidcAdminGroup.POST("/probe", middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), middleware.RateLimitAuth(), oidcAdmin.Probe)
		oidcAdminGroup.POST("/test-login", middleware.RequireRecentAuthentication(middleware.RecentAuthenticationWindow), middleware.RateLimitAuth(), oidcAdmin.BeginTestLogin)
	}

}
