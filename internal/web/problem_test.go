package web_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/web"
)

// TestProblemsHideInternalErrors prevents storage credentials and implementation details leaking to browsers.
func TestProblemsHideInternalErrors(t *testing.T) {
	response := httptest.NewRecorder()
	web.WriteProblem(response, "request-one", errors.New("postgres://operator:private-password@database"))
	if response.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", response.Code)
	}
	if strings.Contains(response.Body.String(), "private-password") {
		t.Error("problem exposed internal error")
	}
	var problem web.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "internal.error" || problem.RequestID != "request-one" || problem.Type == "" || problem.Title == "" {
		t.Errorf("invalid problem envelope: %+v", problem)
	}
	if response.Header().Get("Content-Type") != "application/problem+json" {
		t.Error("problem has wrong content type")
	}
}

// TestKnownProblemPreservesRecoveryGuidance gives clients stable retry behavior without string matching.
func TestKnownProblemPreservesRecoveryGuidance(t *testing.T) {
	response := httptest.NewRecorder()
	web.WriteProblem(response, "request-two", web.Problem{Status: 409, Code: "operation.conflict", Title: "Workspace changed", Detail: "Refresh the workspace before trying again.", Retryable: true})
	if response.Code != 409 {
		t.Errorf("status = %d, want 409", response.Code)
	}
	var problem web.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if !problem.Retryable || problem.Code != "operation.conflict" {
		t.Errorf("problem = %+v", problem)
	}
}
