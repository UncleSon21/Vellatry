package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxBody = 1 << 20

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func badRequest(msg string) error { return &apiError{http.StatusBadRequest, msg} }
func notFound(msg string) error   { return &apiError{http.StatusNotFound, msg} }
func forbidden(msg string) error  { return &apiError{http.StatusForbidden, msg} }
func conflict(msg string) error   { return &apiError{http.StatusConflict, msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail writes an error response. Unexpected errors are logged by the caller's
// middleware and never echoed to the client.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.status, map[string]string{"error": ae.msg})
		return
	}
	s.Logger.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again."})
}

// decode reads a JSON body into v, rejecting unknown fields and oversize bodies.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("Invalid request body: " + strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}
