package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestIdentity_HasRole(t *testing.T) {
	id := Identity{Roles: []Role{RoleAgent, RoleSeniorAgent}}
	if !id.HasRole(RoleAgent) {
		t.Error("HasRole(agent) = false, want true")
	}
	if !id.HasRole(RoleAdmin, RoleSeniorAgent) {
		t.Error("HasRole(any-of) failed for senior_agent")
	}
	if id.HasRole(RoleAdmin) {
		t.Error("HasRole(admin) = true, want false")
	}
}

func TestFromContext_Empty(t *testing.T) {
	if _, err := FromContext(context.Background()); err == nil {
		t.Fatal("want ErrNoIdentity")
	}
}

func TestRequireRole(t *testing.T) {
	called := false
	h := RequireRole(RoleSupervisor)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	cases := []struct {
		name   string
		ctx    context.Context
		status int
	}{
		{"no identity", context.Background(), http.StatusUnauthorized},
		{
			"wrong role",
			withIdentity(context.Background(), Identity{
				AgentID: uuid.New(), TenantID: uuid.New(),
				Roles: []Role{RoleAgent},
			}),
			http.StatusForbidden,
		},
		{
			"right role",
			withIdentity(context.Background(), Identity{
				AgentID: uuid.New(), TenantID: uuid.New(),
				Roles: []Role{RoleSupervisor},
			}),
			http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx)
			h.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Errorf("status = %d, want %d", rr.Code, tc.status)
			}
			if (rr.Code == http.StatusOK) != called {
				t.Errorf("handler called=%v, expected based on status=%d", called, rr.Code)
			}
		})
	}
}
