// Package model defines the core domain types for CronMon.
// These are pure data structs with no behaviour; all business logic lives
// in the scheduler, repository, and handler packages.
package model

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Named string types
// ---------------------------------------------------------------------------

// Status represents the monitoring state of a Check.
type Status string

const (
	StatusNew    Status = "new"
	StatusUp     Status = "up"
	StatusDown   Status = "down"
	StatusPaused Status = "paused"
)

// PingType represents the intent of a single ping event.
type PingType string

const (
	PingSuccess PingType = "success"
	PingStart   PingType = "start"
	PingFail    PingType = "fail"
)

// AlertType represents the kind of alert being dispatched.
type AlertType string

const (
	AlertDown AlertType = "down"
	AlertUp   AlertType = "up"
	AlertFail AlertType = "fail" // fired when a /fail ping is received and the check has NotifyOnFail=true
)

// ---------------------------------------------------------------------------
// Domain structs
// ---------------------------------------------------------------------------

// Check represents a monitored cron job.
// ID is a UUIDv4 string that also serves as the secret in the ping URL.
type Check struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Slug           *string    `json:"slug,omitempty"` // reserved for future human-readable URLs; nil until set
	Schedule       string     `json:"schedule"`       // 5-field cron expression
	Grace          int        `json:"grace"`          // grace period in minutes; minimum 1
	Status         Status     `json:"status"`
	LastPingAt     *time.Time `json:"last_ping_at"`     // nil until first ping
	NextExpectedAt *time.Time `json:"next_expected_at"` // nil until first ping or creation
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Tags           string     `json:"tags"`           // comma-separated; empty string when none
	NotifyOnFail   bool       `json:"notify_on_fail"` // when true, a /fail ping always fires an AlertFail event
}

// Ping represents a single ping event recorded from a monitored job.
type Ping struct {
	ID        int64     `json:"id"`
	CheckID   string    `json:"check_id"`
	Type      PingType  `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	SourceIP  string    `json:"source_ip"` // empty when not determinable
}

// Channel represents a notification channel (email, Slack webhook, or generic webhook).
// Config is a JSON blob; its required keys vary by Type and are validated at write time.
type Channel struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"` // "email" | "slack" | "webhook"
	Name      string          `json:"name"`
	Config    json.RawMessage `json:"config"` // JSON blob validated by handler layer
	CreatedAt time.Time       `json:"created_at"`
}

// CheckChannel represents the many-to-many join between a Check and a Channel.
type CheckChannel struct {
	CheckID   string `json:"check_id"`
	ChannelID int64  `json:"channel_id"`
}

// Notification records the outcome of a dispatched alert.
// ChannelID is nil when the channel has been deleted (ON DELETE SET NULL).
// Error is nil when the notification was delivered successfully.
type Notification struct {
	ID        int64     `json:"id"`
	CheckID   string    `json:"check_id"`
	ChannelID *int64    `json:"channel_id"` // nil = channel deleted; record preserved for audit
	Type      AlertType `json:"type"`
	SentAt    time.Time `json:"sent_at"`
	Error     *string   `json:"error"` // nil = delivered successfully
}

// AlertEvent is enqueued by the scheduler and consumed by the NotifierWorker.
// One AlertEvent is produced per channel subscribed to the affected check.
type AlertEvent struct {
	Check     Check     `json:"check"`
	Channel   Channel   `json:"channel"`
	AlertType AlertType `json:"alert_type"`
}
