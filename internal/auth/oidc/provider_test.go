package oidcauth_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	oidcauth "github.com/nowen-reader/nowen-reader/internal/auth/oidc"
	"golang.org/x/oauth2"
)

type testOIDCProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu            sync.Mutex
	nonce         string
	audience      any
	azp           string
	authTime      *int64
	atHash        string
	expiresAt     time.Time
	signingKey    *rsa.PrivateKey
	signingKid    string
	jwksKey       *rsa.PrivateKey
	jwksKid       string
	tokenStatus   int
	wantVerifier  string
	seenVerifier  string
	discoveryHits int
}

func newTestOIDCProvider(t *testing.T) *testOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	p := &testOIDCProvider{key: key, audience: "nowen-reader", azp: "nowen-reader"}
	mux := http.NewServeMux()
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		p.discoveryHits++
		p.mu.Unlock()
		writeJSON(w, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		key, kid := p.jwksKey, p.jwksKid
		p.mu.Unlock()
		if key == nil {
			key = p.key
		}
		if kid == "" {
			kid = "test-key"
		}
		exponent := big.NewInt(int64(key.PublicKey.E)).Bytes()
		writeJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA",
			"kid": kid,
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(exponent),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		tokenStatus := p.tokenStatus
		p.mu.Unlock()
		if tokenStatus != 0 {
			http.Error(w, "provider failure", tokenStatus)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.seenVerifier = r.Form.Get("code_verifier")
		nonce, audience, azp, authTime, atHash, expiresAt, signingKey, signingKid := p.nonce, p.audience, p.azp, p.authTime, p.atHash, p.expiresAt, p.signingKey, p.signingKid
		p.mu.Unlock()
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "valid-code" {
			http.Error(w, "invalid grant", http.StatusBadRequest)
			return
		}
		if expiresAt.IsZero() {
			expiresAt = time.Now().Add(5 * time.Minute)
		}
		if signingKey == nil {
			signingKey = p.key
		}
		if signingKid == "" {
			signingKid = "test-key"
		}
		claims := map[string]any{
			"iss":                p.server.URL,
			"sub":                "subject-123",
			"aud":                audience,
			"azp":                azp,
			"exp":                expiresAt.Unix(),
			"iat":                time.Now().Add(-time.Minute).Unix(),
			"nonce":              nonce,
			"preferred_username": "alice",
			"name":               "Alice Reader",
			"email":              "alice@example.com",
			"email_verified":     true,
		}
		if authTime != nil {
			claims["auth_time"] = *authTime
		}
		if atHash != "" {
			claims["at_hash"] = atHash
		}
		writeJSON(w, map[string]any{
			"access_token": "access-token",
			"token_type":   "Bearer",
			"expires_in":   300,
			"id_token":     signJWT(t, signingKey, signingKid, claims),
		})
	})
	return p
}

func (p *testOIDCProvider) setTokenClaims(nonce string, audience any, azp string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nonce, p.audience, p.azp = nonce, audience, azp
}

func (p *testOIDCProvider) setAuthTime(value time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	unix := value.Unix()
	p.authTime = &unix
}

func (p *testOIDCProvider) setAccessTokenHash(value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.atHash = value
}

func (p *testOIDCProvider) setTokenValidity(expiresAt time.Time, signingKey *rsa.PrivateKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expiresAt = expiresAt
	p.signingKey = signingKey
}

func (p *testOIDCProvider) setSigningMaterial(signingKey *rsa.PrivateKey, signingKid string, jwksKey *rsa.PrivateKey, jwksKid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signingKey, p.signingKid, p.jwksKey, p.jwksKid = signingKey, signingKid, jwksKey, jwksKid
}

func (p *testOIDCProvider) setTokenStatus(status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokenStatus = status
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func newRemoteProviderForTest(t *testing.T, fake *testOIDCProvider) *oidcauth.RemoteProvider {
	t.Helper()
	provider, err := oidcauth.NewRemoteProvider(oidcauth.RemoteProviderConfig{
		IssuerURL:    fake.server.URL,
		ClientID:     "nowen-reader",
		ClientSecret: "client-secret",
		RedirectURL:  "https://reader.example.com/api/auth/oidc/callback",
		Scopes:       []string{"openid", "profile", "email"},
	})
	if err != nil {
		t.Fatalf("NewRemoteProvider() error = %v", err)
	}
	return provider
}

func TestRemoteProviderUsesDiscoveryAndAuthorizationCodePKCE(t *testing.T) {
	fake := newTestOIDCProvider(t)
	provider := newRemoteProviderForTest(t, fake)
	if fake.discoveryHits != 0 {
		t.Fatal("provider discovery happened during construction; startup must remain available")
	}

	verifier := strings.Repeat("v", 43)
	authorizationURL, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
		State: "state-123", Nonce: "nonce-123", CodeVerifier: verifier, Purpose: oidcauth.PurposeLogin,
	})
	if err != nil {
		t.Fatalf("AuthorizationURL() error = %v", err)
	}
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"client_id":             "nowen-reader",
		"redirect_uri":          "https://reader.example.com/api/auth/oidc/callback",
		"scope":                 "openid profile email",
		"state":                 "state-123",
		"nonce":                 "nonce-123",
		"code_challenge":        oauth2.S256ChallengeFromVerifier(verifier),
		"code_challenge_method": "S256",
	} {
		if got := query.Get(key); got != want {
			t.Fatalf("authorization parameter %s = %q, want %q", key, got, want)
		}
	}
	if fake.discoveryHits != 1 {
		t.Fatalf("discovery hits = %d, want 1", fake.discoveryHits)
	}

	fake.setTokenClaims("nonce-123", "nowen-reader", "nowen-reader")
	identity, err := provider.Exchange(context.Background(), "valid-code", "verifier-123")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if identity.Issuer != fake.server.URL || identity.Subject != "subject-123" || identity.Nonce != "nonce-123" || !identity.EmailVerified {
		t.Fatalf("verified identity = %+v", identity)
	}
	if fake.seenVerifier != "verifier-123" {
		t.Fatalf("token endpoint verifier = %q, want verifier-123", fake.seenVerifier)
	}
}

func TestRemoteProviderForcesInteractiveReauthenticationAndReadsAuthTime(t *testing.T) {
	fake := newTestOIDCProvider(t)
	provider := newRemoteProviderForTest(t, fake)
	authorizationURL, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
		State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeReauth,
	})
	if err != nil {
		t.Fatalf("AuthorizationURL() error = %v", err)
	}
	query, err := url.ParseQuery(strings.SplitN(authorizationURL, "?", 2)[1])
	if err != nil {
		t.Fatalf("parse authorization query: %v", err)
	}
	if query.Get("prompt") != "login" || query.Get("max_age") != "0" {
		t.Fatalf("reauth parameters = prompt %q, max_age %q", query.Get("prompt"), query.Get("max_age"))
	}
	authTime := time.Now().UTC().Truncate(time.Second)
	fake.setTokenClaims("nonce", "nowen-reader", "nowen-reader")
	fake.setAuthTime(authTime)
	identity, err := provider.Exchange(context.Background(), "valid-code", "verifier")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if !identity.AuthTime.Equal(authTime) {
		t.Fatalf("AuthTime = %v, want %v", identity.AuthTime, authTime)
	}
}

func TestRemoteProviderRejectsMismatchedAccessTokenHash(t *testing.T) {
	fake := newTestOIDCProvider(t)
	provider := newRemoteProviderForTest(t, fake)
	if _, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
		State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
	}); err != nil {
		t.Fatalf("AuthorizationURL() error = %v", err)
	}
	fake.setTokenClaims("nonce", "nowen-reader", "nowen-reader")
	fake.setAccessTokenHash("invalid-hash")
	if _, err := provider.Exchange(context.Background(), "valid-code", "verifier"); err == nil || !strings.Contains(err.Error(), "access-token hash") {
		t.Fatalf("Exchange() error = %v, want at_hash rejection", err)
	}
}

func TestRemoteProviderRejectsExpiredOrInvalidlySignedIDToken(t *testing.T) {
	for _, tt := range []struct {
		name       string
		expiresAt  time.Time
		foreignKey bool
	}{
		{name: "expired", expiresAt: time.Now().Add(-time.Minute)},
		{name: "invalid signature", expiresAt: time.Now().Add(time.Minute), foreignKey: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := newTestOIDCProvider(t)
			provider := newRemoteProviderForTest(t, fake)
			if _, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
				State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
			}); err != nil {
				t.Fatalf("AuthorizationURL() error = %v", err)
			}
			var signingKey *rsa.PrivateKey
			if tt.foreignKey {
				var err error
				signingKey, err = rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatalf("generate foreign key: %v", err)
				}
			}
			fake.setTokenClaims("nonce", "nowen-reader", "nowen-reader")
			fake.setTokenValidity(tt.expiresAt, signingKey)
			if _, err := provider.Exchange(context.Background(), "valid-code", "verifier"); err == nil {
				t.Fatal("Exchange() accepted an invalid ID token")
			}
		})
	}
}

func TestRemoteProviderEnforcesAuthorizedPartyForMultipleAudiences(t *testing.T) {
	tests := []struct {
		name    string
		azp     string
		wantErr bool
	}{
		{name: "matching azp", azp: "nowen-reader"},
		{name: "missing azp", azp: "", wantErr: true},
		{name: "wrong azp", azp: "other-client", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newTestOIDCProvider(t)
			provider := newRemoteProviderForTest(t, fake)
			if _, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin}); err != nil {
				t.Fatalf("AuthorizationURL() error = %v", err)
			}
			fake.setTokenClaims("nonce", []string{"nowen-reader", "another-api"}, tt.azp)
			_, err := provider.Exchange(context.Background(), "valid-code", "verifier")
			if tt.wantErr && (err == nil || !strings.Contains(err.Error(), "authorized party")) {
				t.Fatalf("Exchange() error = %v, want authorized party rejection", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Exchange() error = %v", err)
			}
		})
	}
}

func TestRemoteProviderRejectsAudienceMismatch(t *testing.T) {
	fake := newTestOIDCProvider(t)
	provider := newRemoteProviderForTest(t, fake)
	if _, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
		State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
	}); err != nil {
		t.Fatalf("AuthorizationURL() error = %v", err)
	}
	fake.setTokenClaims("nonce", "other-client", "other-client")
	if _, err := provider.Exchange(context.Background(), "valid-code", "verifier"); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("Exchange() error = %v, want audience rejection", err)
	}
}

func TestRemoteProviderRejectsIssuerMismatchInDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                "https://attacker.example",
			"authorization_endpoint":                "https://attacker.example/authorize",
			"token_endpoint":                        "https://attacker.example/token",
			"jwks_uri":                              "https://attacker.example/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	t.Cleanup(server.Close)
	provider, err := oidcauth.NewRemoteProvider(oidcauth.RemoteProviderConfig{
		IssuerURL: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://reader.example/callback",
	})
	if err != nil {
		t.Fatalf("NewRemoteProvider() error = %v", err)
	}
	_, err = provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin})
	if err == nil || !strings.Contains(strings.ToLower(fmt.Sprint(err)), "issuer") {
		t.Fatalf("issuer mismatch error = %v", err)
	}
}

func TestRemoteProviderRejectsInsecureDiscoveredEndpoints(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                server.URL,
			"authorization_endpoint":                "http://127.0.0.1/authorize",
			"token_endpoint":                        "http://127.0.0.1/token",
			"jwks_uri":                              "http://127.0.0.1/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	t.Cleanup(server.Close)
	provider, err := oidcauth.NewRemoteProvider(oidcauth.RemoteProviderConfig{
		IssuerURL: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://reader.example/callback",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewRemoteProvider() error = %v", err)
	}
	_, err = provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
		State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
	})
	if !errors.Is(err, oidcauth.ErrProviderUnavailable) || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("AuthorizationURL() error = %v, want insecure endpoint rejection", err)
	}
}

func TestRemoteProviderBoundsDiscoveryTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	provider, err := oidcauth.NewRemoteProvider(oidcauth.RemoteProviderConfig{
		IssuerURL: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://reader.example/callback",
		HTTPClient: server.Client(), Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRemoteProvider() error = %v", err)
	}
	started := time.Now()
	errorsByRequest := make(chan error, 2)
	for range 2 {
		go func() {
			_, requestErr := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
				State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
			})
			errorsByRequest <- requestErr
		}()
	}
	for range 2 {
		if requestErr := <-errorsByRequest; !errors.Is(requestErr, oidcauth.ErrProviderUnavailable) {
			t.Fatalf("AuthorizationURL() error = %v, want provider unavailable", requestErr)
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("discovery timeout took %v, want a bounded failure", elapsed)
	}
}

func TestRemoteProviderMapsProvider5xxToUnavailable(t *testing.T) {
	t.Run("discovery", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(server.Close)
		provider, err := oidcauth.NewRemoteProvider(oidcauth.RemoteProviderConfig{
			IssuerURL: server.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://reader.example/callback",
		})
		if err != nil {
			t.Fatalf("NewRemoteProvider() error = %v", err)
		}
		_, err = provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
			State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
		})
		if !errors.Is(err, oidcauth.ErrProviderUnavailable) {
			t.Fatalf("AuthorizationURL() error = %v, want provider unavailable", err)
		}
	})

	t.Run("token endpoint", func(t *testing.T) {
		fake := newTestOIDCProvider(t)
		provider := newRemoteProviderForTest(t, fake)
		if _, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
			State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
		}); err != nil {
			t.Fatalf("AuthorizationURL() error = %v", err)
		}
		fake.setTokenStatus(http.StatusBadGateway)
		if _, err := provider.Exchange(context.Background(), "valid-code", "verifier"); !errors.Is(err, oidcauth.ErrProviderUnavailable) {
			t.Fatalf("Exchange() error = %v, want provider unavailable", err)
		}
	})
}

func TestRemoteProviderRefreshesJWKSOnKeyRotationAndRejectsUnknownKey(t *testing.T) {
	fake := newTestOIDCProvider(t)
	provider := newRemoteProviderForTest(t, fake)
	if _, err := provider.AuthorizationURL(context.Background(), oidcauth.AuthorizationRequest{
		State: "state", Nonce: "nonce", CodeVerifier: strings.Repeat("v", 43), Purpose: oidcauth.PurposeLogin,
	}); err != nil {
		t.Fatalf("AuthorizationURL() error = %v", err)
	}
	fake.setTokenClaims("nonce", "nowen-reader", "nowen-reader")
	if _, err := provider.Exchange(context.Background(), "valid-code", "verifier"); err != nil {
		t.Fatalf("initial Exchange() error = %v", err)
	}

	rotatedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rotated key: %v", err)
	}
	fake.setSigningMaterial(rotatedKey, "rotated-key", rotatedKey, "rotated-key")
	if _, err := provider.Exchange(context.Background(), "valid-code", "verifier"); err != nil {
		t.Fatalf("Exchange() after JWKS rotation error = %v", err)
	}

	unknownKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate unknown key: %v", err)
	}
	fake.setSigningMaterial(unknownKey, "unknown-key", rotatedKey, "rotated-key")
	if _, err := provider.Exchange(context.Background(), "valid-code", "verifier"); err == nil {
		t.Fatal("Exchange() accepted an ID token signed by a key absent from JWKS")
	}
}
