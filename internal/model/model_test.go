package model_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/myrrolinz/cronmon/internal/model"
)

// ---------------------------------------------------------------------------
// Named type constants
// ---------------------------------------------------------------------------

func TestStatusConstants(t *testing.T) {
	tests := []struct {
		status model.Status
		want   string
	}{
		{model.StatusNew, "new"},
		{model.StatusUp, "up"},
		{model.StatusDown, "down"},
		{model.StatusPaused, "paused"},
	}
	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("Status constant: got %q, want %q", string(tt.status), tt.want)
		}
	}
}

func TestPingTypeConstants(t *testing.T) {
	tests := []struct {
		pt   model.PingType
		want string
	}{
		{model.PingSuccess, "success"},
		{model.PingStart, "start"},
		{model.PingFail, "fail"},
	}
	for _, tt := range tests {
		if string(tt.pt) != tt.want {
			t.Errorf("PingType constant: got %q, want %q", string(tt.pt), tt.want)
		}
	}
}

func TestAlertTypeConstants(t *testing.T) {
	tests := []struct {
		at   model.AlertType
		want string
	}{
		{model.AlertDown, "down"},
		{model.AlertUp, "up"},
	}
	for _, tt := range tests {
		if string(tt.at) != tt.want {
			t.Errorf("AlertType constant: got %q, want %q", string(tt.at), tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Zero-value tests
// ---------------------------------------------------------------------------

func TestCheckZeroValue(t *testing.T) {
	var c model.Check
	if c.ID != "" {
		t.Errorf("zero Check.ID = %q, want empty string", c.ID)
	}
	if c.Slug != nil {
		t.Errorf("zero Check.Slug = %v, want nil", c.Slug)
	}
	if c.Grace != 0 {
		t.Errorf("zero Check.Grace = %d, want 0", c.Grace)
	}
	if c.Status != "" {
		t.Errorf("zero Check.Status = %q, want empty string", c.Status)
	}
	if c.LastPingAt != nil {
		t.Errorf("zero Check.LastPingAt = %v, want nil", c.LastPingAt)
	}
	if c.NextExpectedAt != nil {
		t.Errorf("zero Check.NextExpectedAt = %v, want nil", c.NextExpectedAt)
	}
}

func TestPingZeroValue(t *testing.T) {
	var p model.Ping
	if p.ID != 0 {
		t.Errorf("zero Ping.ID = %d, want 0", p.ID)
	}
	if p.CheckID != "" {
		t.Errorf("zero Ping.CheckID = %q, want empty string", p.CheckID)
	}
	if p.Type != "" {
		t.Errorf("zero Ping.Type = %q, want empty string", p.Type)
	}
}

func TestChannelZeroValue(t *testing.T) {
	var ch model.Channel
	if ch.ID != 0 {
		t.Errorf("zero Channel.ID = %d, want 0", ch.ID)
	}
	if ch.Config != nil {
		t.Errorf("zero Channel.Config = %s, want nil", ch.Config)
	}
}

func TestNotificationZeroValue(t *testing.T) {
	var n model.Notification
	if n.ID != 0 {
		t.Errorf("zero Notification.ID = %d, want 0", n.ID)
	}
	if n.ChannelID != nil {
		t.Errorf("zero Notification.ChannelID = %v, want nil", n.ChannelID)
	}
	if n.Error != nil {
		t.Errorf("zero Notification.Error = %v, want nil", n.Error)
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip tests
// ---------------------------------------------------------------------------

func TestCheckJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	original := model.Check{
		ID:             "a3f9c2d1-0000-0000-0000-000000000001",
		Name:           "Database backup",
		Slug:           nil,
		Schedule:       "0 2 * * *",
		Grace:          10,
		Status:         model.StatusUp,
		LastPingAt:     &now,
		NextExpectedAt: &now,
		CreatedAt:      now,
		UpdatedAt:      now,
		Tags:           "prod,backup",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal Check: %v", err)
	}

	var got model.Check
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal Check: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("Check.ID: got %q, want %q", got.ID, original.ID)
	}
	if got.Name != original.Name {
		t.Errorf("Check.Name: got %q, want %q", got.Name, original.Name)
	}
	if got.Schedule != original.Schedule {
		t.Errorf("Check.Schedule: got %q, want %q", got.Schedule, original.Schedule)
	}
	if got.Grace != original.Grace {
		t.Errorf("Check.Grace: got %d, want %d", got.Grace, original.Grace)
	}
	if got.Status != original.Status {
		t.Errorf("Check.Status: got %q, want %q", got.Status, original.Status)
	}
	if got.Tags != original.Tags {
		t.Errorf("Check.Tags: got %q, want %q", got.Tags, original.Tags)
	}
	if got.LastPingAt == nil || !got.LastPingAt.Equal(*original.LastPingAt) {
		t.Errorf("Check.LastPingAt: got %v, want %v", got.LastPingAt, original.LastPingAt)
	}
	if got.NextExpectedAt == nil || !got.NextExpectedAt.Equal(*original.NextExpectedAt) {
		t.Errorf("Check.NextExpectedAt: got %v, want %v", got.NextExpectedAt, original.NextExpectedAt)
	}
	if !got.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("Check.CreatedAt: got %v, want %v", got.CreatedAt, original.CreatedAt)
	}
}

func TestCheckJSONRoundTripNilPointers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	original := model.Check{
		ID:        "abc-nil",
		Name:      "no pings yet",
		Schedule:  "* * * * *",
		Grace:     5,
		Status:    model.StatusNew,
		CreatedAt: now,
		UpdatedAt: now,
		// LastPingAt and NextExpectedAt are nil
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal Check (nil ptrs): %v", err)
	}

	var got model.Check
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal Check (nil ptrs): %v", err)
	}

	if got.LastPingAt != nil {
		t.Errorf("Check.LastPingAt: got %v, want nil", got.LastPingAt)
	}
	if got.NextExpectedAt != nil {
		t.Errorf("Check.NextExpectedAt: got %v, want nil", got.NextExpectedAt)
	}
}

func TestPingJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	original := model.Ping{
		ID:        42,
		CheckID:   "a3f9c2d1-0000-0000-0000-000000000001",
		Type:      model.PingSuccess,
		CreatedAt: now,
		SourceIP:  "192.0.2.1",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal Ping: %v", err)
	}

	var got model.Ping
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal Ping: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("Ping.ID: got %d, want %d", got.ID, original.ID)
	}
	if got.CheckID != original.CheckID {
		t.Errorf("Ping.CheckID: got %q, want %q", got.CheckID, original.CheckID)
	}
	if got.Type != original.Type {
		t.Errorf("Ping.Type: got %q, want %q", got.Type, original.Type)
	}
	if got.SourceIP != original.SourceIP {
		t.Errorf("Ping.SourceIP: got %q, want %q", got.SourceIP, original.SourceIP)
	}
	if !got.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("Ping.CreatedAt: got %v, want %v", got.CreatedAt, original.CreatedAt)
	}
}

func TestChannelJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	original := model.Channel{
		ID:        7,
		Type:      "email",
		Name:      "ops alerts",
		Config:    json.RawMessage(`{"address":"ops@example.com"}`),
		CreatedAt: now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal Channel: %v", err)
	}

	var got model.Channel
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal Channel: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("Channel.ID: got %d, want %d", got.ID, original.ID)
	}
	if got.Type != original.Type {
		t.Errorf("Channel.Type: got %q, want %q", got.Type, original.Type)
	}
	if got.Name != original.Name {
		t.Errorf("Channel.Name: got %q, want %q", got.Name, original.Name)
	}
	if string(got.Config) != string(original.Config) {
		t.Errorf("Channel.Config: got %s, want %s", got.Config, original.Config)
	}
	if !got.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("Channel.CreatedAt: got %v, want %v", got.CreatedAt, original.CreatedAt)
	}
}

func TestCheckChannelJSONRoundTrip(t *testing.T) {
	original := model.CheckChannel{
		CheckID:   "a3f9c2d1-0000-0000-0000-000000000001",
		ChannelID: 3,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal CheckChannel: %v", err)
	}

	var got model.CheckChannel
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal CheckChannel: %v", err)
	}

	if got.CheckID != original.CheckID {
		t.Errorf("CheckChannel.CheckID: got %q, want %q", got.CheckID, original.CheckID)
	}
	if got.ChannelID != original.ChannelID {
		t.Errorf("CheckChannel.ChannelID: got %d, want %d", got.ChannelID, original.ChannelID)
	}
}

func TestNotificationJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	channelID := int64(7)
	errMsg := "connection refused"

	original := model.Notification{
		ID:        99,
		CheckID:   "a3f9c2d1-0000-0000-0000-000000000001",
		ChannelID: &channelID,
		Type:      model.AlertDown,
		SentAt:    now,
		Error:     &errMsg,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal Notification: %v", err)
	}

	var got model.Notification
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal Notification: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("Notification.ID: got %d, want %d", got.ID, original.ID)
	}
	if got.Type != original.Type {
		t.Errorf("Notification.Type: got %q, want %q", got.Type, original.Type)
	}
	if got.ChannelID == nil {
		t.Errorf("Notification.ChannelID: got nil, want %d", channelID)
	} else if *got.ChannelID != channelID {
		t.Errorf("Notification.ChannelID: got %d, want %d", *got.ChannelID, channelID)
	}
	if got.Error == nil {
		t.Errorf("Notification.Error: got nil, want %q", errMsg)
	} else if *got.Error != errMsg {
		t.Errorf("Notification.Error: got %q, want %q", *got.Error, errMsg)
	}
	if !got.SentAt.Equal(original.SentAt) {
		t.Errorf("Notification.SentAt: got %v, want %v", got.SentAt, original.SentAt)
	}
}

func TestNotificationJSONRoundTripNilFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	original := model.Notification{
		ID:        1,
		CheckID:   "abc",
		ChannelID: nil, // channel deleted
		Type:      model.AlertUp,
		SentAt:    now,
		Error:     nil, // delivered successfully
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal Notification (nil fields): %v", err)
	}

	var got model.Notification
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal Notification (nil fields): %v", err)
	}

	if got.ChannelID != nil {
		t.Errorf("Notification.ChannelID: got %v, want nil", got.ChannelID)
	}
	if got.Error != nil {
		t.Errorf("Notification.Error: got %v, want nil", got.Error)
	}
}

func TestAlertEventJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	original := model.AlertEvent{
		Check: model.Check{
			ID:        "a3f9c2d1-0000-0000-0000-000000000001",
			Name:      "Database backup",
			Schedule:  "0 2 * * *",
			Grace:     10,
			Status:    model.StatusDown,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Channel: model.Channel{
			ID:        7,
			Type:      "email",
			Name:      "ops",
			Config:    json.RawMessage(`{"address":"ops@example.com"}`),
			CreatedAt: now,
		},
		AlertType: model.AlertDown,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal AlertEvent: %v", err)
	}

	var got model.AlertEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal AlertEvent: %v", err)
	}

	if got.Check.ID != original.Check.ID {
		t.Errorf("AlertEvent.Check.ID: got %q, want %q", got.Check.ID, original.Check.ID)
	}
	if got.Channel.Type != original.Channel.Type {
		t.Errorf("AlertEvent.Channel.Type: got %q, want %q", got.Channel.Type, original.Channel.Type)
	}
	if got.AlertType != original.AlertType {
		t.Errorf("AlertEvent.AlertType: got %q, want %q", got.AlertType, original.AlertType)
	}
}
