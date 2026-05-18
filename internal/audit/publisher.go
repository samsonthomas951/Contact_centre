package audit

import "context"

// Publisher is the producer-side seam other packages depend on so
// they don't have to import nats / jetstream directly. The gateway
// wires a concrete JetStreamPublisher; tests pass NoopPublisher.
//
// Failure semantics: Publish should NEVER fail the caller's
// operation. The hash chain is a record, not a precondition.
// Implementations log and return the error; callers log+swallow.
type Publisher interface {
	Publish(ctx context.Context, e *Event) error
}

// NoopPublisher discards every event. Useful when audit is
// optional in a binary (e.g. cmd/seed) or in unit tests that don't
// care about the chain.
type NoopPublisher struct{}

func (NoopPublisher) Publish(_ context.Context, _ *Event) error { return nil }

// FuncPublisher adapts a function to Publisher so call sites can
// inject a quick spy without defining a type.
type FuncPublisher func(ctx context.Context, e *Event) error

func (f FuncPublisher) Publish(ctx context.Context, e *Event) error { return f(ctx, e) }
