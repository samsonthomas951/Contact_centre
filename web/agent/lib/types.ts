// Mirrors the JSON shapes returned by the Go gateway. Kept narrow on
// purpose -- only the fields the UI consumes appear here, so the type
// system catches a backend-contract drift the same day it lands.

export type TicketState =
  | "new" | "open" | "pending" | "on_hold"
  | "resolved" | "closed" | "reopened";

export type Channel = "fb" | "x" | "wa" | "ig" | "widget" | "voice";

export interface Ticket {
  id: string;
  tenant_id: string;
  conversation_id: string;
  state: TicketState;
  priority: number;
  required_skills: string[];
  assigned_agent_id?: string;
  sla_first_response_due?: string;
  sla_resolution_due?: string;
  first_response_at?: string;
  resolved_at?: string;
  closed_at?: string;
  created_at: string;
  updated_at: string;
}

export type Direction = "in" | "out" | "note";

export interface Message {
  id: number;
  ticket_id: string;
  direction: Direction;
  agent_id?: string;
  body: string;
  attachments: string[];
  platform_message_id?: string;
  created_at: string;
}

export interface QueueDepth {
  state: TicketState;
  count: number;
}

export interface AgentPresence {
  agent_id: string;
  display_name: string;
  status: "online" | "away" | "break" | "offline";
  current_load: number;
  max_concurrent: number;
  last_heartbeat: string;
}

export interface AtRiskTicket {
  ticket_id: string;
  priority: number;
  state: TicketState;
  assigned_agent_id?: string;
  first_response_due?: string;
  resolution_due?: string;
  created_at: string;
}
