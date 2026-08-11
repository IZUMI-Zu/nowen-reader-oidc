package oidcruntime

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAESGCMSecretProtectorRoundTripUsesRandomNonceAndRejectsTampering(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	protector, err := NewAESGCMSecretProtector(key, SecretProtectionExternalKey)
	if err != nil {
		t.Fatalf("NewAESGCMSecretProtector() error = %v", err)
	}

	first, keyID, err := protector.Encrypt([]byte("client-secret"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	second, _, err := protector.Encrypt([]byte("client-secret"))
	if err != nil {
		t.Fatalf("second Encrypt() error = %v", err)
	}
	if first == second || keyID == "" || !strings.HasPrefix(first, "v1:"+keyID+":") {
		t.Fatalf("unexpected envelopes first=%q second=%q keyID=%q", first, second, keyID)
	}
	plaintext, err := protector.Decrypt(first)
	if err != nil || string(plaintext) != "client-secret" {
		t.Fatalf("Decrypt() = %q, %v", plaintext, err)
	}

	replacement := "A"
	if strings.HasSuffix(first, replacement) {
		replacement = "B"
	}
	tampered := first[:len(first)-1] + replacement
	if _, err := protector.Decrypt(tampered); err == nil {
		t.Fatal("Decrypt() accepted tampered ciphertext")
	}
}

func TestReadConcurrentlyCreatedSecretKeyWaitsForCompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oidc-config.key")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, writeErr := file.Write([]byte("0123456789abcdef0123456789abcdef"))
		if writeErr == nil {
			writeErr = file.Close()
		}
		written <- writeErr
	}()
	key, err := readConcurrentlyCreatedSecretKey(path)
	if err != nil || string(key) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("readConcurrentlyCreatedSecretKey() = %q, %v", key, err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func TestReadOrCreateLocalSecretKeyWaitsWhenCreatedFileIsIncomplete(t *testing.T) {
	keyValue := []byte("0123456789abcdef0123456789abcdef")
	for name, prefixLength := range map[string]int{"empty": 0, "partial": 8} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "oidc-config.key")
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write(keyValue[:prefixLength]); err != nil {
				t.Fatal(err)
			}
			written := make(chan error, 1)
			go func() {
				time.Sleep(20 * time.Millisecond)
				_, writeErr := file.Write(keyValue[prefixLength:])
				if writeErr == nil {
					writeErr = file.Close()
				}
				written <- writeErr
			}()
			key, readErr := readOrCreateLocalSecretKey(path)
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			if readErr != nil || string(key) != string(keyValue) {
				t.Fatalf("readOrCreateLocalSecretKey() = %q, %v", key, readErr)
			}
		})
	}
}

func TestFileSecretProtectorUsesExternalOrProtectedLocalKey(t *testing.T) {
	t.Run("external base64 key", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "mounted-secret")
		encoded := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")) + "\n"
		if err := os.WriteFile(keyPath, []byte(encoded), 0o600); err != nil {
			t.Fatal(err)
		}
		protector, err := NewFileSecretProtector(dir, keyPath)
		if err != nil || protector.Protection() != SecretProtectionExternalKey {
			t.Fatalf("NewFileSecretProtector() = %+v, %v", protector, err)
		}
	})

	t.Run("local key is stable and mode 0600", func(t *testing.T) {
		dir := t.TempDir()
		first, err := NewFileSecretProtector(dir, "")
		if err != nil {
			t.Fatalf("first NewFileSecretProtector() error = %v", err)
		}
		second, err := NewFileSecretProtector(dir, "")
		if err != nil {
			t.Fatalf("second NewFileSecretProtector() error = %v", err)
		}
		if first.KeyID() != second.KeyID() || first.Protection() != SecretProtectionLocalKey {
			t.Fatalf("local key was not stable: %q != %q", first.KeyID(), second.KeyID())
		}
		info, err := os.Stat(filepath.Join(dir, "secrets", "oidc-config.key"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("local key mode = %o, want 600", info.Mode().Perm())
		}
	})
}
