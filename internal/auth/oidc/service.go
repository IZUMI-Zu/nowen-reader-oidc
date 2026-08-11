package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

var (
	ErrInvalidReturnTo      = errors.New("invalid OIDC return target")
	ErrInvalidTransaction   = errors.New("invalid or expired OIDC transaction")
	ErrInvalidIdentity      = errors.New("invalid OIDC identity")
	ErrProviderUnavailable  = errors.New("OIDC provider unavailable")
	ErrConfigurationChanged = errors.New("OIDC configuration changed")
)

type Purpose string

const (
	PurposeLogin      Purpose = "login"
	PurposeLink       Purpose = "link"
	PurposeReauth     Purpose = "reauth"
	PurposeConfigTest Purpose = "config_test"
)

type BeginRequest struct {
	Purpose       Purpose
	SessionUserID string
	SessionID     string
	ReturnTo      string
}

type AuthorizationRedirect struct {
	URL          string
	BindingToken string
	ExpiresAt    time.Time
	CookieSecure bool
}

type CallbackRequest struct {
	State        string
	Code         string
	BindingToken string
}

type CancelRequest struct {
	State        string
	BindingToken string
}

type LoginTransaction struct {
	StateHash         string
	BindingHash       string
	Nonce             string
	PKCEVerifier      string
	Purpose           Purpose
	SessionUserID     string
	SessionID         string
	ReturnTo          string
	ExpiresAt         time.Time
	CreatedAt         time.Time
	ConfigRevision    int64
	ConfigFingerprint string
}

type VerifiedIdentity struct {
	Issuer            string
	Subject           string
	Nonce             string
	AuthTime          time.Time
	PreferredUsername string
	DisplayName       string
	Email             string
	EmailVerified     bool
}

type AuthenticatedIdentity struct {
	VerifiedIdentity
	Purpose           Purpose
	SessionUserID     string
	SessionID         string
	ReturnTo          string
	ConfigRevision    int64
	ConfigFingerprint string
}

type AuthorizationRequest struct {
	State        string
	Nonce        string
	CodeVerifier string
	Purpose      Purpose
}

// Provider is the narrow adapter around an OIDC implementation. Production
// code uses go-oidc/x/oauth2; tests use a fake at this public seam.
type Provider interface {
	Issuer() string
	AuthorizationURL(ctx context.Context, request AuthorizationRequest) (string, error)
	Exchange(ctx context.Context, code, codeVerifier string) (VerifiedIdentity, error)
}

// TransactionStore persists browser-bound one-time authorization state. A
// successful Consume must be atomic and may return a transaction only once.
type TransactionStore interface {
	Create(ctx context.Context, transaction LoginTransaction) error
	Consume(ctx context.Context, stateHash, bindingHash string, now time.Time) (LoginTransaction, error)
}

type Options struct {
	BasePath          string
	TransactionTTL    time.Duration
	Now               func() time.Time
	ConfigRevision    int64
	ConfigFingerprint string
}

type Service struct {
	provider          Provider
	transactions      TransactionStore
	basePath          string
	ttl               time.Duration
	now               func() time.Time
	configRevision    int64
	configFingerprint string
}

func NewService(provider Provider, transactions TransactionStore, options Options) *Service {
	ttl := options.TransactionTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	basePath := strings.TrimSuffix(options.BasePath, "/")
	if basePath == "" {
		basePath = "/"
	}
	return &Service{
		provider: provider, transactions: transactions, basePath: basePath, ttl: ttl, now: now,
		configRevision: options.ConfigRevision, configFingerprint: options.ConfigFingerprint,
	}
}

func (s *Service) Begin(ctx context.Context, request BeginRequest) (AuthorizationRedirect, error) {
	if !validPurpose(request.Purpose) || (request.Purpose == PurposeLogin) != (request.SessionUserID == "") ||
		(request.Purpose == PurposeConfigTest) != (request.SessionID != "") {
		return AuthorizationRedirect{}, fmt.Errorf("%w: unsupported purpose", ErrInvalidTransaction)
	}
	returnTo, err := validateReturnTo(request.ReturnTo, s.basePath)
	if err != nil {
		return AuthorizationRedirect{}, err
	}
	state, err := randomToken(32)
	if err != nil {
		return AuthorizationRedirect{}, fmt.Errorf("generate state: %w", err)
	}
	nonce, err := randomToken(32)
	if err != nil {
		return AuthorizationRedirect{}, fmt.Errorf("generate nonce: %w", err)
	}
	verifier := oauth2.GenerateVerifier()
	binding, err := randomToken(32)
	if err != nil {
		return AuthorizationRedirect{}, fmt.Errorf("generate browser binding: %w", err)
	}
	authorizationURL, err := s.provider.AuthorizationURL(ctx, AuthorizationRequest{
		State: state, Nonce: nonce, CodeVerifier: verifier, Purpose: request.Purpose,
	})
	if err != nil {
		return AuthorizationRedirect{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	now := s.now().UTC()
	transaction := LoginTransaction{
		StateHash:         hashToken(state),
		BindingHash:       hashToken(binding),
		Nonce:             nonce,
		PKCEVerifier:      verifier,
		Purpose:           request.Purpose,
		SessionUserID:     request.SessionUserID,
		SessionID:         request.SessionID,
		ReturnTo:          returnTo,
		CreatedAt:         now,
		ExpiresAt:         now.Add(s.ttl),
		ConfigRevision:    s.configRevision,
		ConfigFingerprint: s.configFingerprint,
	}
	if err := s.transactions.Create(ctx, transaction); err != nil {
		return AuthorizationRedirect{}, fmt.Errorf("persist OIDC transaction: %w", err)
	}
	return AuthorizationRedirect{URL: authorizationURL, BindingToken: binding, ExpiresAt: transaction.ExpiresAt}, nil
}

func (s *Service) Complete(ctx context.Context, request CallbackRequest) (AuthenticatedIdentity, error) {
	if request.State == "" || request.Code == "" || request.BindingToken == "" {
		return AuthenticatedIdentity{}, ErrInvalidTransaction
	}
	transaction, err := s.transactions.Consume(ctx, hashToken(request.State), hashToken(request.BindingToken), s.now().UTC())
	if err != nil {
		if errors.Is(err, ErrInvalidTransaction) {
			return AuthenticatedIdentity{}, ErrInvalidTransaction
		}
		return AuthenticatedIdentity{}, fmt.Errorf("consume OIDC transaction: %w", err)
	}
	result := AuthenticatedIdentity{
		Purpose: transaction.Purpose, SessionUserID: transaction.SessionUserID, SessionID: transaction.SessionID, ReturnTo: transaction.ReturnTo,
		ConfigRevision: transaction.ConfigRevision, ConfigFingerprint: transaction.ConfigFingerprint,
	}
	if !constantTimeEqual(transaction.ConfigFingerprint, s.configFingerprint) {
		return result, ErrConfigurationChanged
	}
	identity, err := s.provider.Exchange(ctx, request.Code, transaction.PKCEVerifier)
	if err != nil {
		if errors.Is(err, ErrProviderUnavailable) {
			return result, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
		}
		return result, fmt.Errorf("OIDC code exchange: %w", err)
	}
	if identity.Issuer != s.provider.Issuer() || identity.Subject == "" || !constantTimeEqual(identity.Nonce, transaction.Nonce) {
		return result, ErrInvalidIdentity
	}
	if transaction.Purpose == PurposeReauth || transaction.Purpose == PurposeConfigTest {
		const clockSkew = 2 * time.Minute
		now := s.now().UTC()
		if identity.AuthTime.IsZero() || identity.AuthTime.Before(transaction.CreatedAt.Add(-clockSkew)) || identity.AuthTime.After(now.Add(clockSkew)) {
			return result, ErrInvalidIdentity
		}
	}
	result.VerifiedIdentity = identity
	return result, nil
}

// Cancel consumes a browser-bound authorization transaction without
// exchanging a code. It is used when the Provider returns an OAuth error so
// the browser can safely return to the original local page.
func (s *Service) Cancel(ctx context.Context, request CancelRequest) (string, error) {
	if request.State == "" || request.BindingToken == "" {
		return "", ErrInvalidTransaction
	}
	transaction, err := s.transactions.Consume(ctx, hashToken(request.State), hashToken(request.BindingToken), s.now().UTC())
	if err != nil {
		if errors.Is(err, ErrInvalidTransaction) {
			return "", ErrInvalidTransaction
		}
		return "", fmt.Errorf("consume cancelled OIDC transaction: %w", err)
	}
	if !constantTimeEqual(transaction.ConfigFingerprint, s.configFingerprint) {
		return transaction.ReturnTo, ErrConfigurationChanged
	}
	return transaction.ReturnTo, nil
}

func validPurpose(purpose Purpose) bool {
	return purpose == PurposeLogin || purpose == PurposeLink || purpose == PurposeReauth || purpose == PurposeConfigTest
}

func validateReturnTo(raw, basePath string) (string, error) {
	if raw == "" {
		if basePath == "/" {
			return "/", nil
		}
		return basePath + "/", nil
	}
	if strings.Contains(raw, "\\") || strings.HasPrefix(raw, "//") {
		return "", ErrInvalidReturnTo
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", ErrInvalidReturnTo
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || strings.Contains(decodedPath, "\\") || strings.IndexFunc(decodedPath, func(value rune) bool { return value < 0x20 || value == 0x7f }) >= 0 {
		return "", ErrInvalidReturnTo
	}
	for _, segment := range strings.Split(decodedPath, "/") {
		if segment == "." || segment == ".." {
			return "", ErrInvalidReturnTo
		}
	}
	if basePath != "/" && decodedPath != basePath && !strings.HasPrefix(decodedPath, basePath+"/") {
		return "", ErrInvalidReturnTo
	}
	return raw, nil
}

func randomToken(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashToken(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func constantTimeEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
