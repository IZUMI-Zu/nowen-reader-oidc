package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newSecureProbeContext(remoteAddr, forwardedProto string, directTLS bool) *gin.Context {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodGet, "http://reader.example.com/", nil)
	request.RemoteAddr = remoteAddr
	if forwardedProto != "" {
		request.Header.Set("X-Forwarded-Proto", forwardedProto)
	}
	if directTLS {
		request.TLS = &tls.ConnectionState{}
	}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request
	return context
}

// TestForwardedProtoIsHTTPS exercises the pure parser directly so every edge
// case is unambiguous, independent of proxy trust configuration.
func TestForwardedProtoIsHTTPS(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "exact https", value: "https", want: true},
		{name: "case insensitive", value: "HTTPS", want: true},
		{name: "surrounding whitespace", value: "  https  ", want: true},
		{name: "plain http", value: "http", want: false},
		{name: "empty", value: "", want: false},
		{name: "client https then proxy http", value: "https, http", want: true},
		{name: "client http then proxy https", value: "http, https", want: false},
		{name: "scheme with slashes", value: "https://", want: false},
		{name: "repeated https", value: "https,https", want: true},
		{name: "leading empty entry", value: " , https", want: false},
		{name: "unrelated token", value: "not-a-proto", want: false},
		{name: "substring trap", value: "xhttpsx", want: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := forwardedProtoIsHTTPS(testCase.value); got != testCase.want {
				t.Fatalf("forwardedProtoIsHTTPS(%q) = %v, want %v", testCase.value, got, testCase.want)
			}
		})
	}
}

func TestIsRequestSecure(t *testing.T) {
	trustNone := func(t *testing.T) {
		t.Setenv("TRUST_PROXY_HEADERS", "")
		t.Setenv("TRUSTED_PROXIES", "")
	}
	trustLoopback := func(t *testing.T) {
		t.Setenv("TRUST_PROXY_HEADERS", "true")
		t.Setenv("TRUSTED_PROXIES", "127.0.0.1")
	}
	trustCIDR := func(t *testing.T) {
		t.Setenv("TRUST_PROXY_HEADERS", "true")
		t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8")
	}

	tests := []struct {
		name       string
		directTLS  bool
		remoteAddr string
		forwarded  string
		setup      func(*testing.T)
		want       bool
	}{
		{name: "direct TLS is secure", directTLS: true, remoteAddr: "203.0.113.9:1234", setup: trustNone, want: true},
		{name: "direct TLS wins over spoofed header", directTLS: true, remoteAddr: "203.0.113.9:1234", forwarded: "http", setup: trustNone, want: true},
		{name: "plain http no proxy no header", directTLS: false, remoteAddr: "203.0.113.9:1234", setup: trustNone, want: false},
		{name: "spoofed https without trusted proxy", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "https", setup: trustNone, want: false},
		{name: "trusted proxy https", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "https", setup: trustLoopback, want: true},
		{name: "trusted proxy plain http", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "http", setup: trustLoopback, want: false},
		{name: "untrusted peer even with proxy config", directTLS: false, remoteAddr: "203.0.113.9:1234", forwarded: "https", setup: trustLoopback, want: false},
		{name: "trusted proxy empty proto", directTLS: false, remoteAddr: "127.0.0.1:1234", setup: trustLoopback, want: false},
		{name: "CIDR trusted proxy https", directTLS: false, remoteAddr: "10.1.2.3:1234", forwarded: "https", setup: trustCIDR, want: true},
		{name: "CIDR outside range rejected", directTLS: false, remoteAddr: "192.168.1.2:1234", forwarded: "https", setup: trustCIDR, want: false},
		{name: "leftmost http wins", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "http, https", setup: trustLoopback, want: false},
		{name: "leftmost https wins", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "https, http", setup: trustLoopback, want: true},
		{name: "case insensitive HTTPS", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "HTTPS", setup: trustLoopback, want: true},
		{name: "substring trap rejected", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "xhttpsx", setup: trustLoopback, want: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.setup(t)
			got := IsRequestSecure(newSecureProbeContext(testCase.remoteAddr, testCase.forwarded, testCase.directTLS))
			if got != testCase.want {
				t.Fatalf("IsRequestSecure() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestSecurityHeadersHSTSPolicy(t *testing.T) {
	trustNone := func(t *testing.T) {
		t.Setenv("TRUST_PROXY_HEADERS", "")
		t.Setenv("TRUSTED_PROXIES", "")
	}
	trustLoopback := func(t *testing.T) {
		t.Setenv("TRUST_PROXY_HEADERS", "true")
		t.Setenv("TRUSTED_PROXIES", "127.0.0.1")
	}

	tests := []struct {
		name       string
		directTLS  bool
		remoteAddr string
		forwarded  string
		setup      func(*testing.T)
		wantHSTS   bool
	}{
		{name: "direct TLS sets HSTS", directTLS: true, remoteAddr: "203.0.113.9:1234", setup: trustNone, wantHSTS: true},
		{name: "trusted proxy https sets HSTS", directTLS: false, remoteAddr: "127.0.0.1:1234", forwarded: "https", setup: trustLoopback, wantHSTS: true},
		{name: "spoofed https without trusted proxy omits HSTS", directTLS: false, remoteAddr: "203.0.113.9:1234", forwarded: "https", setup: trustNone, wantHSTS: false},
		{name: "plain http loopback omits HSTS", directTLS: false, remoteAddr: "127.0.0.1:1234", setup: trustNone, wantHSTS: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.setup(t)
			context := newSecureProbeContext(testCase.remoteAddr, testCase.forwarded, testCase.directTLS)
			SecurityHeaders()(context)
			got := context.Writer.Header().Get("Strict-Transport-Security")
			if (got != "") != testCase.wantHSTS {
				t.Fatalf("Strict-Transport-Security = %q, wantHSTS %v", got, testCase.wantHSTS)
			}
		})
	}
}
