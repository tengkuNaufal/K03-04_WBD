package web

import (
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	r := httptest.NewRequest("POST", "http://app.local/x", nil)
	if !sameOrigin(r) {
		t.Error("no Origin header should pass (non-browser client)")
	}
	r.Header.Set("Origin", "http://app.local")
	if !sameOrigin(r) {
		t.Error("same origin rejected")
	}
	r.Header.Set("Origin", "http://evil.test")
	if sameOrigin(r) {
		t.Error("cross origin accepted")
	}
	r.Header.Set("Origin", "null")
	if sameOrigin(r) {
		t.Error("opaque origin accepted")
	}
	r = httptest.NewRequest("POST", "http://app.local/x", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if sameOrigin(r) {
		t.Error("cross-site fetch accepted")
	}
}

func TestValidCSRF(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set(csrfHeader, "abc")
	if !validCSRF(r, "abc") {
		t.Error("matching token rejected")
	}
	if validCSRF(r, "other") {
		t.Error("wrong token accepted")
	}
	r = httptest.NewRequest("POST", "/x", nil)
	if validCSRF(r, "") || validCSRF(r, "abc") {
		t.Error("missing token accepted")
	}
}
