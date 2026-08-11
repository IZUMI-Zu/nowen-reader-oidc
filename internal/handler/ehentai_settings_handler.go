package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nowen-reader/nowen-reader/internal/config"
)

// EHentaiSettingsHandler manages non-sensitive EH/EX source settings. Account
// cookies remain environment-only and are never accepted or returned here.
type EHentaiSettingsHandler struct{}

func NewEHentaiSettingsHandler() *EHentaiSettingsHandler {
	return &EHentaiSettingsHandler{}
}

type ehentaiSettingsResponse struct {
	config.EHentaiSettings
	CredentialsConfigured bool   `json:"credentialsConfigured"`
	ConfigurationValid    bool   `json:"configurationValid"`
	ConfigurationError    string `json:"configurationError,omitempty"`
}

// Get returns settings and a masked credential status to administrators.
func (h *EHentaiSettingsHandler) Get(c *gin.Context) {
	settings := config.GetSiteConfig().ResolvedEHentaiSettings()
	c.JSON(http.StatusOK, buildEHentaiSettingsResponse(settings))
}

// Update validates and persists non-sensitive source settings.
func (h *EHentaiSettingsHandler) Update(c *gin.Context) {
	var body config.EHentaiSettings
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	settings, err := config.ValidateEHentaiSettings(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if settings.Enabled {
		if _, err := config.ResolveEHentaiConfig(settings); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	current := config.GetSiteConfig()
	current.EHentai = &settings
	if err := config.SaveSiteConfig(&current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save E-Hentai settings"})
		return
	}

	c.JSON(http.StatusOK, buildEHentaiSettingsResponse(settings))
}

func buildEHentaiSettingsResponse(settings config.EHentaiSettings) ehentaiSettingsResponse {
	response := ehentaiSettingsResponse{EHentaiSettings: settings, ConfigurationValid: true}
	configured, credentialErr := config.EHentaiCredentialsConfigured()
	response.CredentialsConfigured = configured
	if credentialErr != nil {
		response.ConfigurationValid = false
		response.ConfigurationError = credentialErr.Error()
		return response
	}
	if settings.Enabled {
		if _, err := config.ResolveEHentaiConfig(settings); err != nil {
			response.ConfigurationValid = false
			response.ConfigurationError = err.Error()
		}
	}
	return response
}
