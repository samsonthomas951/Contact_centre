package auth

import (
	"testing"

	"github.com/google/uuid"
)

func TestExtractIdentity_HappyPath(t *testing.T) {
	cfg := Config{
		TenantClaim: "tenant_id",
		RolesClaim:  "roles",
	}
	agent := uuid.New()
	tenant := uuid.New()
	claims := map[string]any{
		"sub":       agent.String(),
		"tenant_id": tenant.String(),
		"email":     "agent@example.co.ke",
		"roles":     []any{"agent", "senior_agent", "robot"},
	}
	id, err := extractIdentity(claims, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if id.AgentID != agent {
		t.Errorf("AgentID = %v, want %v", id.AgentID, agent)
	}
	if id.TenantID != tenant {
		t.Errorf("TenantID = %v, want %v", id.TenantID, tenant)
	}
	if !id.HasRole(RoleAgent) || !id.HasRole(RoleSeniorAgent) {
		t.Errorf("missing expected roles: %v", id.Roles)
	}
	for _, r := range id.Roles {
		if string(r) == "robot" {
			t.Error("'robot' should be filtered as unknown role")
		}
	}
}

func TestExtractIdentity_MissingClaims(t *testing.T) {
	cfg := Config{TenantClaim: "tenant_id", RolesClaim: "roles"}
	cases := []struct {
		name   string
		claims map[string]any
	}{
		{"no sub", map[string]any{"tenant_id": uuid.NewString()}},
		{"sub not uuid", map[string]any{"sub": "joe", "tenant_id": uuid.NewString()}},
		{"no tenant", map[string]any{"sub": uuid.NewString()}},
		{"tenant not uuid", map[string]any{"sub": uuid.NewString(), "tenant_id": "tenant-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := extractIdentity(tc.claims, cfg); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestParseRoles_Shapes(t *testing.T) {
	wantAgent := []Role{RoleAgent}
	cases := []struct {
		name string
		in   any
		want []Role
	}{
		{"[]any", []any{"agent"}, wantAgent},
		{"[]string", []string{"agent"}, wantAgent},
		{"comma-string", "agent,senior_agent", []Role{RoleAgent, RoleSeniorAgent}},
		{"space-string", "agent senior_agent", []Role{RoleAgent, RoleSeniorAgent}},
		{"map", map[string]any{"agent": true, "supervisor": true}, []Role{RoleAgent, RoleSupervisor}},
		{"unknown filtered", []any{"agent", "ninja"}, wantAgent},
		{"dedup", []any{"agent", "agent"}, wantAgent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRoles(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			// order from map iteration is non-deterministic; compare as a set
			for _, w := range tc.want {
				found := false
				for _, g := range got {
					if g == w {
						found = true
					}
				}
				if !found {
					t.Errorf("missing role %v in %v", w, got)
				}
			}
		})
	}
}
