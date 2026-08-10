package oidcruntime

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	secretEnvelopeVersion = "v1"
	secretAssociatedData  = "nowen-reader:oidc-provider-config:1"
	secretKeyBytes        = 32
)

type AESGCMSecretProtector struct {
	aead       cipher.AEAD
	keyID      string
	protection SecretProtection
}

func NewAESGCMSecretProtector(key []byte, protection SecretProtection) (*AESGCMSecretProtector, error) {
	if len(key) != secretKeyBytes {
		return nil, fmt.Errorf("OIDC configuration encryption key must contain exactly %d bytes", secretKeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create OIDC secret cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("create OIDC secret AEAD: %w", err)
	}
	digest := sha256.Sum256(key)
	return &AESGCMSecretProtector{
		aead: aead, keyID: hex.EncodeToString(digest[:8]), protection: protection,
	}, nil
}

// NewFileSecretProtector loads an externally mounted key when keyFile is set.
// Otherwise it creates or reuses a local 0600 key under dataDir.
func NewFileSecretProtector(dataDir, keyFile string) (*AESGCMSecretProtector, error) {
	keyFile = strings.TrimSpace(keyFile)
	if keyFile != "" {
		key, err := readSecretKey(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read external OIDC configuration key: %w", err)
		}
		return NewAESGCMSecretProtector(key, SecretProtectionExternalKey)
	}

	keyPath := filepath.Join(dataDir, "secrets", "oidc-config.key")
	key, err := readOrCreateLocalSecretKey(keyPath)
	if err != nil {
		return nil, err
	}
	return NewAESGCMSecretProtector(key, SecretProtectionLocalKey)
}

func (p *AESGCMSecretProtector) Encrypt(plaintext []byte) (string, string, error) {
	if len(plaintext) == 0 {
		return "", "", errors.New("OIDC client secret cannot be empty")
	}
	sealed := p.aead.Seal(nil, nil, plaintext, []byte(secretAssociatedData))
	encoded := base64.RawURLEncoding.EncodeToString(sealed)
	return strings.Join([]string{secretEnvelopeVersion, p.keyID, encoded}, ":"), p.keyID, nil
}

func (p *AESGCMSecretProtector) Decrypt(ciphertext string) ([]byte, error) {
	parts := strings.Split(ciphertext, ":")
	if len(parts) != 3 || parts[0] != secretEnvelopeVersion || parts[1] != p.keyID {
		return nil, errors.New("OIDC client secret envelope is unsupported or uses a different key")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(sealed) != parts[2] {
		return nil, errors.New("OIDC client secret envelope is invalid")
	}
	plaintext, err := p.aead.Open(nil, nil, sealed, []byte(secretAssociatedData))
	if err != nil {
		return nil, errors.New("OIDC client secret could not be decrypted")
	}
	return plaintext, nil
}

func (p *AESGCMSecretProtector) KeyID() string { return p.keyID }

func (p *AESGCMSecretProtector) Protection() SecretProtection { return p.protection }

func readOrCreateLocalSecretKey(path string) ([]byte, error) {
	if key, err := readSecretKey(path); err == nil {
		if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
			return nil, fmt.Errorf("protect local OIDC configuration key: %w", chmodErr)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read local OIDC configuration key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create OIDC secret directory: %w", err)
	}
	key := make([]byte, secretKeyBytes)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate OIDC configuration key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readConcurrentlyCreatedSecretKey(path)
	}
	if err != nil {
		return nil, fmt.Errorf("create local OIDC configuration key: %w", err)
	}
	written := false
	defer func() {
		_ = file.Close()
		if !written {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(key); err != nil {
		return nil, fmt.Errorf("write local OIDC configuration key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync local OIDC configuration key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close local OIDC configuration key: %w", err)
	}
	written = true
	return key, nil
}

func readConcurrentlyCreatedSecretKey(path string) ([]byte, error) {
	const (
		attempts = 100
		delay    = 10 * time.Millisecond
	)
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		var key []byte
		key, err = readSecretKey(path)
		if err == nil {
			return key, nil
		}
		if attempt+1 < attempts {
			time.Sleep(delay)
		}
	}
	return nil, fmt.Errorf("read concurrently created local OIDC configuration key: %w", err)
}

func readSecretKey(path string) ([]byte, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(value) == secretKeyBytes {
		return append([]byte(nil), value...), nil
	}
	trimmed := strings.TrimSpace(string(value))
	if len(trimmed) == secretKeyBytes {
		return []byte(trimmed), nil
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		decoded, decodeErr := encoding.DecodeString(trimmed)
		if decodeErr == nil && len(decoded) == secretKeyBytes {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("key file must contain 32 raw bytes or their base64 encoding")
}
