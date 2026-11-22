package web

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Problem is intentionally public recovery guidance, never an underlying error string.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Code      string            `json:"code"`
	Detail    string            `json:"detail,omitempty"`
	RequestID string            `json:"requestId"`
	Retryable bool              `json:"retryable"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// Error exposes only the stable code when a public problem crosses package boundaries.
func (p Problem) Error() string { return p.Code }

// WriteProblem replaces unexpected errors with a fixed message and server request ID.
func WriteProblem(w http.ResponseWriter, requestID string, err error) {
	var problem Problem
	var pointer *Problem
	if errors.As(err, &pointer) && pointer != nil {
		problem = *pointer
	} else if !errors.As(err, &problem) {
		problem = Problem{}
	}
	if problem.Status < 400 || problem.Status > 599 || problem.Code == "" || problem.Title == "" {
		problem = Problem{Status: 500, Code: "internal.error", Title: "Unable to complete the request", Detail: "Try again. If the problem continues, contact your administrator."}
	}
	problem.Type = "about:blank"
	problem.RequestID = requestID
	encoded, encodeErr := json.Marshal(problem)
	if encodeErr != nil {
		http.Error(w, "Unable to complete the request", 500)
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(problem.Status)
	_, _ = w.Write(encoded) // A disconnected client cannot receive a replacement response.
}
