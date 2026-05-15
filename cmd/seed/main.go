// Command seed populates a freshly-migrated database with one tenant,
// two agents, five customers and five tickets so the agent UI has
// something to render on first login. Idempotent on the deterministic
// UUIDs in demo.go -- re-running is safe.
//
// Run via:
//
//	make demo-seed
//
// (which is `go run ./cmd/seed` with DATABASE_URL exported).
//
// The deterministic agent UUIDs match what the agent UI's NextAuth
// stub provider issues so a "demo:<agent>:<tenant>:agent" bearer
// reaches a real row.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/samsonthomas951/contact-centre/internal/pkg/config"
	"github.com/samsonthomas951/contact-centre/internal/pkg/logging"
	"github.com/samsonthomas951/contact-centre/internal/pkg/postgres"
)

var version = "dev"

// Deterministic UUIDs so the demo bearer (issued by the agent UI's
// stub login) matches an actual row. Don't change these without also
// updating web/agent/.env.demo.
var (
	DemoTenantID     = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	DemoAgentAdaID   = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	DemoAgentBobID   = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	DemoAgentCarolID = uuid.MustParse("44444444-4444-4444-4444-444444444444")
	DemoAgentDianaID = uuid.MustParse("55555555-5555-5555-5555-555555555555")
)

func main() {
	if err := run(); err != nil {
		slog.Error("seed: fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	logging.Init(logging.Options{Service: "seed", Version: version, Level: slog.LevelInfo})

	var dbCfg postgres.Config
	if err := config.Load("", &dbCfg); err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := postgres.Connect(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := upsertTenant(ctx, tx); err != nil {
		return err
	}
	if err := upsertAgents(ctx, tx); err != nil {
		return err
	}
	if err := upsertConversationsAndTickets(ctx, tx); err != nil {
		return err
	}
	if err := upsertWidgetSite(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	slog.Info("seed: ok",
		slog.String("tenant", DemoTenantID.String()),
		slog.String("agent_ada", DemoAgentAdaID.String()),
		slog.String("agent_bob", DemoAgentBobID.String()),
		slog.String("agent_carol", DemoAgentCarolID.String()),
		slog.String("agent_diana", DemoAgentDianaID.String()),
	)
	return nil
}

func upsertTenant(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tenants (id, slug, display_name)
		VALUES ($1, 'acme', 'Acme Demo')
		ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name`,
		DemoTenantID)
	return err
}

func upsertAgents(ctx context.Context, tx pgx.Tx) error {
	type agent struct {
		ID    uuid.UUID
		Email string
		Name  string
		Role  string
	}
	for _, a := range []agent{
		{DemoAgentAdaID, "ada@demo.local", "Ada Lovelace", "agent"},
		{DemoAgentBobID, "bob@demo.local", "Bob Supervisor", "supervisor"},
		{DemoAgentCarolID, "carol@demo.local", "Carol Admin", "admin"},
		{DemoAgentDianaID, "diana@demo.local", "Diana DPO", "dpo"},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agents (id, tenant_id, email, display_name, role, max_concurrent)
			VALUES ($1, $2, $3, $4, $5, 5)
			ON CONFLICT (tenant_id, email) DO UPDATE
			  SET display_name = EXCLUDED.display_name,
			      role         = EXCLUDED.role,
			      active       = TRUE`,
			a.ID, DemoTenantID, a.Email, a.Name, a.Role); err != nil {
			return fmt.Errorf("seed: agent %s: %w", a.Email, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO agent_status (agent_id, status, current_load, last_heartbeat)
			VALUES ($1, 'online', 0, now())
			ON CONFLICT (agent_id) DO UPDATE
			  SET status = 'online', last_heartbeat = now()`,
			a.ID); err != nil {
			return fmt.Errorf("seed: agent_status %s: %w", a.Email, err)
		}
	}
	return nil
}

func upsertConversationsAndTickets(ctx context.Context, tx pgx.Tx) error {
	type ticketSeed struct {
		CustomerName     string
		CustomerExternal string
		Channel          string
		Priority         int16
		State            string
		FirstMessage     string
		AgeHours         int
		AssignTo         *uuid.UUID
	}
	ada := DemoAgentAdaID
	now := time.Now().UTC()

	seeds := []ticketSeed{
		{"Wanjiru Kamau", "PSID_001", "fb", 1, "new", "Hi, my account is locked and I cant access my balance. Please help.", 0, nil},
		{"Otieno Owino", "X_USR_42", "x", 2, "open", "Hey @brand any update on my refund? Filed 3 days ago.", 1, &ada},
		{"Achieng Akoth", "254799000111", "wa", 3, "open", "Hello, I'd like to upgrade my plan to the family bundle.", 2, &ada},
		{"Kipchoge Mutai", "PSID_004", "fb", 4, "pending", "Just confirming the appointment for Friday. Thanks.", 3, nil},
		{"Atieno Adhiambo", "IGSID_005", "ig", 3, "open", "Saw your post -- is the offer still valid this week?", 5, nil},
	}

	// Stable customer + conversation IDs derived from the external ref
	// so re-running is idempotent (uuid.NewSHA1 with a fixed namespace).
	ns := uuid.MustParse("99999999-9999-9999-9999-999999999999")

	for i, s := range seeds {
		customerID := uuid.NewSHA1(ns, []byte("c:"+s.Channel+":"+s.CustomerExternal))
		conversationID := uuid.NewSHA1(ns, []byte("v:"+s.Channel+":"+s.CustomerExternal))
		ticketID := uuid.NewSHA1(ns, fmt.Appendf(nil, "t:%d:%s:%s", i, s.Channel, s.CustomerExternal))

		// Customer.
		if _, err := tx.Exec(ctx, `
			INSERT INTO customers (id, tenant_id, display_name, external_refs)
			VALUES ($1, $2, $3, jsonb_build_object($4::text, $5::text))
			ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name`,
			customerID, DemoTenantID, s.CustomerName, s.Channel, s.CustomerExternal); err != nil {
			return fmt.Errorf("seed: customer %s: %w", s.CustomerExternal, err)
		}
		// Conversation.
		if _, err := tx.Exec(ctx, `
			INSERT INTO conversations (id, tenant_id, customer_id, channel, channel_thread_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, channel, channel_thread_id) DO UPDATE
			  SET customer_id = EXCLUDED.customer_id`,
			conversationID, DemoTenantID, customerID, s.Channel, s.CustomerExternal); err != nil {
			return fmt.Errorf("seed: conversation %s: %w", s.CustomerExternal, err)
		}
		// Ticket.
		createdAt := now.Add(-time.Duration(s.AgeHours) * time.Hour)
		var slaFirst, slaRes *time.Time
		// Match the production policy: priority 3 -> 1h FR, 24h res.
		fr := createdAt.Add(time.Hour)
		res := createdAt.Add(24 * time.Hour)
		slaFirst, slaRes = &fr, &res

		if _, err := tx.Exec(ctx, `
			INSERT INTO tickets
			  (id, tenant_id, conversation_id, state, priority,
			   sla_first_response_due, sla_resolution_due,
			   assigned_agent_id, created_at)
			VALUES ($1, $2, $3, $4::ticket_state, $5, $6, $7, $8, $9)
			ON CONFLICT (id) DO UPDATE
			  SET state = EXCLUDED.state, priority = EXCLUDED.priority,
			      assigned_agent_id = EXCLUDED.assigned_agent_id`,
			ticketID, DemoTenantID, conversationID, s.State, s.Priority,
			slaFirst, slaRes, s.AssignTo, createdAt); err != nil {
			return fmt.Errorf("seed: ticket %d: %w", i, err)
		}

		// First message (inbound) -- deduped on platform_message_id.
		platformMsgID := fmt.Sprintf("seed.%s.%d", s.Channel, i)
		if err := upsertMessage(ctx, tx, ticketID, "in", nil, s.FirstMessage, platformMsgID, createdAt); err != nil {
			return err
		}
		// If assigned, add an agent reply to make the thread look real.
		if s.AssignTo != nil {
			reply := "Hi " + firstName(s.CustomerName) + ", thanks for reaching out -- looking into this now."
			if err := upsertMessage(ctx, tx, ticketID, "out", s.AssignTo,
				reply, fmt.Sprintf("seed.reply.%d", i),
				createdAt.Add(15*time.Minute)); err != nil {
				return err
			}
		}
	}
	return nil
}

func upsertMessage(ctx context.Context, tx pgx.Tx, ticketID uuid.UUID, direction string,
	agentID *uuid.UUID, body, platformID string, createdAt time.Time,
) error {
	// EXISTS guard -> idempotent on platform_message_id.
	var seen bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM messages
		  WHERE tenant_id = $1 AND platform_message_id = $2
		)`, DemoTenantID, platformID).Scan(&seen); err != nil {
		return err
	}
	if seen {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO messages
		  (tenant_id, ticket_id, direction, agent_id, body,
		   attachments, platform_message_id, created_at)
		VALUES ($1, $2, $3, $4, $5, ARRAY[]::uuid[], $6, $7)`,
		DemoTenantID, ticketID, direction, agentID, body, platformID, createdAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("seed: message %s: %w", platformID, err)
	}
	return nil
}

// upsertWidgetSite registers the demo HTML page (served at
// http://localhost:8000) as an allowed Origin for the embeddable
// widget. The widget's WS upgrade rejects any other origin -- this
// lets the demo page connect without bypassing the CSRF check.
func upsertWidgetSite(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO widget_sites
		  (tenant_id, origin, display_name, welcome_message, embed_key, active)
		VALUES ($1, 'http://localhost:8000', 'Acme Demo Site',
		        'Hi! How can we help today?', 'demo-key', TRUE)
		ON CONFLICT (tenant_id, origin) DO UPDATE
		  SET display_name = EXCLUDED.display_name,
		      welcome_message = EXCLUDED.welcome_message,
		      embed_key = EXCLUDED.embed_key,
		      active = TRUE`,
		DemoTenantID)
	return err
}

func firstName(full string) string {
	for i, c := range full {
		if c == ' ' {
			return full[:i]
		}
	}
	return full
}
