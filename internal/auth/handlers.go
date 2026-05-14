package auth

import (
	"encoding/json"
	"net/http"
)

// MeHandler returns the verified Identity as JSON. Mounted behind
// Middleware so an unauthenticated request never reaches it.
func MeHandler(w http.ResponseWriter, r *http.Request) {
	id, err := FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp := struct {
		AgentID  string   `json:"agent_id"`
		TenantID string   `json:"tenant_id"`
		Email    string   `json:"email,omitempty"`
		Roles    []string `json:"roles"`
	}{
		AgentID:  id.AgentID.String(),
		TenantID: id.TenantID.String(),
		Email:    id.Email,
		Roles:    rolesAsStrings(id.Roles),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func rolesAsStrings(rs []Role) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
