package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A second entry for the same key:value is a conflict (409), not a server error.
func TestCreateLabelVocabulary_DuplicateIsConflict(t *testing.T) {
	srv := testServer()
	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/labels",
			strings.NewReader(`{"key":"project","value":"alpha"}`))
		rec := httptest.NewRecorder()
		srv.handleCreateLabelVocabulary(rec, req)
		return rec.Code
	}
	if code := post(); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("first create: status %d", code)
	}
	if code := post(); code != http.StatusConflict {
		t.Errorf("duplicate create: status %d, want %d", code, http.StatusConflict)
	}
}
