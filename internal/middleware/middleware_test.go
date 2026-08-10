package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestCORSMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(CORS())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	// Regular request should have CORS headers
	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("Expected Access-Control-Allow-Origin header")
	}

	// OPTIONS request should return 204
	req, _ = http.NewRequest("OPTIONS", "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS should return 204, got %d", w.Code)
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(SecurityHeaders())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	expectedHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "SAMEORIGIN",
		"X-XSS-Protection":       "1; mode=block",
	}

	for header, expected := range expectedHeaders {
		actual := w.Header().Get(header)
		if actual != expected {
			t.Errorf("Expected %s: '%s', got '%s'", header, expected, actual)
		}
	}
}

func TestGzipMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(Gzip())
	r.GET("/test", func(c *gin.Context) {
		// Return enough data to trigger compression (> minCompressLength)
		data := make([]byte, 2000)
		for i := range data {
			data[i] = 'a'
		}
		c.Header("Content-Type", "text/plain")
		c.Data(200, "text/plain", data)
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("Expected Content-Encoding: gzip, got: %v", w.Header())
	}

	// Verify response is valid gzip
	reader, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("Failed to create gzip reader: %v", err)
	}
	defer reader.Close()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to decompress: %v", err)
	}
	if len(decompressed) != 2000 {
		t.Errorf("Expected 2000 bytes decompressed, got %d", len(decompressed))
	}
}

func TestGzipMiddlewareSkipsSmallResponses(t *testing.T) {
	r := gin.New()
	r.Use(Gzip())
	r.GET("/test", func(c *gin.Context) {
		c.Data(200, "text/plain", []byte("small"))
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Small response should not be compressed
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Error("Small responses should not be gzip compressed")
	}
}

func TestGzipMiddlewareSkipsBinaryContent(t *testing.T) {
	r := gin.New()
	r.Use(Gzip())
	r.GET("/test", func(c *gin.Context) {
		data := make([]byte, 2000)
		c.Header("Content-Type", "image/png")
		c.Data(200, "image/png", data)
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Image content should not be compressed
	if w.Header().Get("Content-Encoding") == "gzip" {
		t.Error("Image responses should not be gzip compressed")
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(Recovery())
	r.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	req, _ := http.NewRequest("GET", "/panic", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected 500 after panic, got %d", w.Code)
	}
}

func TestRequestLoggerMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(RequestLogger())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}

func TestRequestAndErrorLoggersRedactOIDCCallbackCredentials(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&output)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	errorLogBuffer.Clear()
	t.Cleanup(errorLogBuffer.Clear)

	r := gin.New()
	r.Use(RequestLogger(), ErrorLogCapture())
	r.GET("/reader/api/auth/oidc/callback", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid callback"})
	})
	request := httptest.NewRequest(http.MethodGet,
		"/reader/api/auth/oidc/callback?code=secret-code&state=secret-state&error_description=cancelled", nil)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	if strings.Contains(output.String(), "secret-code") || strings.Contains(output.String(), "secret-state") {
		t.Fatalf("request log leaked OIDC callback credentials: %s", output.String())
	}
	entries := errorLogBuffer.GetAll()
	if len(entries) != 1 || strings.Contains(entries[0].Path, "secret-code") || strings.Contains(entries[0].Path, "secret-state") || !strings.Contains(entries[0].Path, "REDACTED") {
		t.Fatalf("error log did not redact OIDC callback: %#v", entries)
	}
}

func TestQuietLoggerMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(QuietLogger())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(200, gin.H{"ok": true})
	})
	r.GET("/api/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Regular request
	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	// Health check should also work (but be silenced in logs)
	req, _ = http.NewRequest("GET", "/api/health", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}
