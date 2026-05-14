// Package ctxkeys defines the typed keys used to carry request-scoped
// metadata across package boundaries.
//
// Using a dedicated unexported type prevents collisions with other
// packages and is the idiomatic Go pattern for context values.
package ctxkeys

type key int

// Keys carried on every authenticated request.
const (
	CorrelationID key = iota + 1
	TenantID
	AgentID
	AgentRoles
)
