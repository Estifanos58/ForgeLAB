package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/forgelab/backend/internal/middleware"
)

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// getUserIDFromContext extracts the authenticated user ID from the request context.
func getUserIDFromContext(r *http.Request) (uuid.UUID, bool) {
	return middleware.GetUserID(r.Context())
}
