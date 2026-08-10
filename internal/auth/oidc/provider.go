package oidcauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type RemoteProviderConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	HTTPClient   *http.Client
	Timeout      time.Duration
}

// RemoteProvider is a lazy go-oidc/x/oauth2 adapter. Discovery is intentionally
// deferred until an OIDC request so a provider outage cannot prevent local
// authentication or application startup.
type RemoteProvider struct {
	config RemoteProviderConfig

	mu          sync.Mutex
	oauthConfig *oauth2.Config
	verifier    *coreoidc.IDTokenVerifier
	discovering chan struct{}
	httpClient  *http.Client
	timeout     time.Duration
}

func NewRemoteProvider(config RemoteProviderConfig) (*RemoteProvider, error) {
	config.IssuerURL = strings.TrimSpace(config.IssuerURL)
	config.RedirectURL = strings.TrimSpace(config.RedirectURL)
	if config.IssuerURL == "" || config.ClientID == "" || config.ClientSecret == "" || config.RedirectURL == "" {
		return nil, errors.New("issuer, client ID, client secret, and redirect URL are required")
	}
	issuer, err := url.Parse(config.IssuerURL)
	if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" ||
		(issuer.Scheme != "https" && !(issuer.Scheme == "http" && isLoopbackHostname(issuer.Hostname()))) {
		return nil, errors.New("issuer must be an absolute HTTPS URL")
	}
	if len(config.Scopes) == 0 {
		config.Scopes = []string{coreoidc.ScopeOpenID, "profile", "email"}
	}
	if !containsString(config.Scopes, coreoidc.ScopeOpenID) {
		config.Scopes = append([]string{coreoidc.ScopeOpenID}, config.Scopes...)
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := &http.Client{}
	if config.HTTPClient != nil {
		*client = *config.HTTPClient
	}
	if client.Timeout <= 0 || client.Timeout > timeout {
		client.Timeout = timeout
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = providerStatusTransport{base: transport}
	return &RemoteProvider{config: config, httpClient: client, timeout: timeout}, nil
}

func (p *RemoteProvider) Issuer() string {
	return p.config.IssuerURL
}

// Probe performs Discovery and endpoint validation without starting a browser
// authorization transaction. It cannot validate the client secret because
// confidential-client authentication occurs later at the token endpoint.
func (p *RemoteProvider) Probe(ctx context.Context) error {
	ctx, cancel := p.requestContext(ctx)
	defer cancel()
	_, _, err := p.ensure(ctx)
	return err
}

func (p *RemoteProvider) AuthorizationURL(ctx context.Context, request AuthorizationRequest) (string, error) {
	ctx, cancel := p.requestContext(ctx)
	defer cancel()
	oauthConfig, _, err := p.ensure(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	options := []oauth2.AuthCodeOption{
		coreoidc.Nonce(request.Nonce),
		oauth2.S256ChallengeOption(request.CodeVerifier),
	}
	if request.Purpose == PurposeReauth || request.Purpose == PurposeConfigTest {
		options = append(options,
			oauth2.SetAuthURLParam("prompt", "login"),
			oauth2.SetAuthURLParam("max_age", "0"),
		)
	}
	return oauthConfig.AuthCodeURL(request.State, options...), nil
}

func (p *RemoteProvider) Exchange(ctx context.Context, code, codeVerifier string) (VerifiedIdentity, error) {
	ctx, cancel := p.requestContext(ctx)
	defer cancel()
	oauthConfig, verifier, err := p.ensure(ctx)
	if err != nil {
		return VerifiedIdentity{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	token, err := oauthConfig.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		if providerUnavailable(err) {
			return VerifiedIdentity{}, fmt.Errorf("%w: exchange authorization code: %v", ErrProviderUnavailable, err)
		}
		return VerifiedIdentity{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return VerifiedIdentity{}, errors.New("token response did not contain an ID token")
	}
	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		if providerUnavailable(err) {
			return VerifiedIdentity{}, fmt.Errorf("%w: verify ID token: %v", ErrProviderUnavailable, err)
		}
		return VerifiedIdentity{}, fmt.Errorf("verify ID token: %w", err)
	}
	if idToken.AccessTokenHash != "" {
		if err := idToken.VerifyAccessToken(token.AccessToken); err != nil {
			return VerifiedIdentity{}, fmt.Errorf("verify ID token access-token hash: %w", err)
		}
	}
	var claims struct {
		Nonce             string `json:"nonce"`
		AuthTime          *int64 `json:"auth_time"`
		AuthorizedParty   string `json:"azp"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
		EmailVerified     bool   `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return VerifiedIdentity{}, fmt.Errorf("decode verified ID token claims: %w", err)
	}
	if len(idToken.Audience) > 1 && claims.AuthorizedParty == "" {
		return VerifiedIdentity{}, errors.New("ID token with multiple audiences is missing authorized party")
	}
	if claims.AuthorizedParty != "" && claims.AuthorizedParty != p.config.ClientID {
		return VerifiedIdentity{}, errors.New("ID token authorized party does not match client ID")
	}
	identity := VerifiedIdentity{
		Issuer:            idToken.Issuer,
		Subject:           idToken.Subject,
		Nonce:             claims.Nonce,
		PreferredUsername: claims.PreferredUsername,
		DisplayName:       claims.Name,
		Email:             claims.Email,
		EmailVerified:     claims.EmailVerified,
	}
	if claims.AuthTime != nil {
		identity.AuthTime = time.Unix(*claims.AuthTime, 0).UTC()
	}
	return identity, nil
}

func (p *RemoteProvider) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	return coreoidc.ClientContext(ctx, p.httpClient), cancel
}

type providerStatusTransport struct {
	base http.RoundTripper
}

func (t providerStatusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		_ = response.Body.Close()
		return nil, providerHTTPStatusError{code: response.StatusCode, status: response.Status}
	}
	return response, nil
}

type providerHTTPStatusError struct {
	code   int
	status string
}

func (e providerHTTPStatusError) Error() string { return "OIDC provider returned " + e.status }

func providerUnavailable(err error) bool {
	var statusError providerHTTPStatusError
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		(errors.As(err, &statusError) && (statusError.code == http.StatusTooManyRequests || statusError.code >= http.StatusInternalServerError)) ||
		errors.As(err, &networkError)
}

func (p *RemoteProvider) ensure(ctx context.Context) (*oauth2.Config, *coreoidc.IDTokenVerifier, error) {
	for {
		p.mu.Lock()
		if p.oauthConfig != nil && p.verifier != nil {
			oauthConfig, verifier := p.oauthConfig, p.verifier
			p.mu.Unlock()
			return oauthConfig, verifier, nil
		}
		if p.discovering != nil {
			discovering := p.discovering
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, nil, fmt.Errorf("wait for OIDC discovery: %w", ctx.Err())
			case <-discovering:
				continue
			}
		}
		discovering := make(chan struct{})
		p.discovering = discovering
		p.mu.Unlock()

		provider, err := coreoidc.NewProvider(ctx, p.config.IssuerURL)
		var oauthConfig *oauth2.Config
		var verifier *coreoidc.IDTokenVerifier
		if err == nil {
			err = validateDiscoveredEndpoints(p.config.IssuerURL, provider)
		}
		if err == nil {
			oauthConfig = &oauth2.Config{
				ClientID:     p.config.ClientID,
				ClientSecret: p.config.ClientSecret,
				Endpoint:     provider.Endpoint(),
				RedirectURL:  p.config.RedirectURL,
				Scopes:       append([]string(nil), p.config.Scopes...),
			}
			verifier = provider.Verifier(&coreoidc.Config{ClientID: p.config.ClientID})
		}

		p.mu.Lock()
		if err == nil {
			p.oauthConfig, p.verifier = oauthConfig, verifier
		}
		p.discovering = nil
		close(discovering)
		p.mu.Unlock()
		if err != nil {
			return nil, nil, fmt.Errorf("discover OIDC provider: %w", err)
		}
		return oauthConfig, verifier, nil
	}
}

func validateDiscoveredEndpoints(issuerRaw string, provider *coreoidc.Provider) error {
	var metadata struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return fmt.Errorf("decode OIDC provider metadata: %w", err)
	}
	issuer, err := url.Parse(issuerRaw)
	if err != nil {
		return fmt.Errorf("parse OIDC issuer: %w", err)
	}
	allowLoopbackHTTP := issuer.Scheme == "http" && isLoopbackHostname(issuer.Hostname())
	for name, raw := range map[string]string{
		"authorization_endpoint": metadata.AuthorizationEndpoint,
		"token_endpoint":         metadata.TokenEndpoint,
		"jwks_uri":               metadata.JWKSURI,
	} {
		endpoint, parseErr := url.Parse(raw)
		if parseErr != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
			return fmt.Errorf("OIDC %s must be an absolute URL without userinfo or fragment", name)
		}
		if endpoint.Scheme == "https" {
			continue
		}
		if !allowLoopbackHTTP || endpoint.Scheme != "http" || !isLoopbackHostname(endpoint.Hostname()) {
			return fmt.Errorf("OIDC %s must use HTTPS", name)
		}
	}
	return nil
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
