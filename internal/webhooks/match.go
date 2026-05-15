package webhooks

import "strings"

// MatchSubject reports whether `subject` matches a NATS-style pattern.
// Patterns:
//
//   "ticket.created"     literal
//   "ticket.*"           one token wildcard
//   "ticket.>"           tail wildcard (matches one or more tokens)
//   ""                   matches nothing
//
// Multiple-pattern matching is provided by MatchAny; this function is
// the per-pattern primitive.
func MatchSubject(pattern, subject string) bool {
	if pattern == "" {
		return false
	}
	if pattern == subject {
		return true
	}
	pTokens := strings.Split(pattern, ".")
	sTokens := strings.Split(subject, ".")

	for i, pt := range pTokens {
		if pt == ">" {
			// Tail wildcard matches the rest unconditionally, but only
			// when there is at least one remaining subject token.
			return i < len(sTokens)
		}
		if i >= len(sTokens) {
			return false
		}
		if pt == "*" {
			continue
		}
		if pt != sTokens[i] {
			return false
		}
	}
	return len(pTokens) == len(sTokens)
}

// MatchAny reports whether subject matches any pattern in patterns.
// An empty pattern slice means "no filter" -- callers can interpret
// that as "subscribe to everything".
func MatchAny(patterns []string, subject string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if MatchSubject(p, subject) {
			return true
		}
	}
	return false
}
