package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateSessionRejectsForeignOrigins(t *testing.T) {
	// A broken origin check must not reach OpenAI with a real key from the environment.
	t.Setenv("OPENAI_API_KEY", "")

	for _, requestOrigin := range []string{"", "https://example.com", "http://127.0.0.1:3000"} {
		t.Run(requestOrigin, func(t *testing.T) {
			req := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				"/api/session",
				strings.NewReader("v=0"),
			)
			if requestOrigin != "" {
				req.Header.Set("Origin", requestOrigin)
			}
			rec := httptest.NewRecorder()

			createSession(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("Origin %q: status %d, want %d", requestOrigin, rec.Code, http.StatusForbidden)
			}
		})
	}
}
