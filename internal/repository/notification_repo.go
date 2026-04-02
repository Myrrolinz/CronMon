package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/myrrolinz/cronmon/internal/model"
)

// NotificationRepository defines read/write operations for Notification records.
type NotificationRepository interface {
	Create(ctx context.Context, n *model.Notification) error
	ListByCheckID(ctx context.Context, checkID string, limit int) ([]*model.Notification, error)
}

type sqliteNotificationRepo struct {
	db *sql.DB
}

// NewNotificationRepository returns a SQLite-backed NotificationRepository.
func NewNotificationRepository(db *sql.DB) NotificationRepository {
	return &sqliteNotificationRepo{db: db}
}

func scanNotification(s rowScanner) (*model.Notification, error) {
	var (
		n             model.Notification
		channelIDNull sql.NullInt64
		typeStr       string
		sentAtStr     string
		errNull       sql.NullString
	)
	err := s.Scan(&n.ID, &n.CheckID, &channelIDNull, &typeStr, &sentAtStr, &errNull)
	if err != nil {
		return nil, err
	}
	n.Type = model.AlertType(typeStr)
	if channelIDNull.Valid {
		n.ChannelID = &channelIDNull.Int64
	}
	n.SentAt, err = parseTime(sentAtStr)
	if err != nil {
		return nil, fmt.Errorf("sent_at: %w", err)
	}
	if errNull.Valid {
		n.Error = &errNull.String
	}
	return &n, nil
}

func (r *sqliteNotificationRepo) Create(ctx context.Context, n *model.Notification) error {
	const q = `
INSERT INTO notifications (check_id, channel_id, type, sent_at, error)
VALUES (?, ?, ?, ?, ?)`

	var channelID sql.NullInt64
	if n.ChannelID != nil {
		channelID = sql.NullInt64{Int64: *n.ChannelID, Valid: true}
	}

	var errStr sql.NullString
	if n.Error != nil {
		errStr = sql.NullString{String: *n.Error, Valid: true}
	}

	res, err := r.db.ExecContext(ctx, q,
		n.CheckID, channelID, string(n.Type), formatTime(n.SentAt), errStr,
	)
	if err != nil {
		return fmt.Errorf("notificationRepo.Create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("notificationRepo.Create last id: %w", err)
	}
	n.ID = id
	return nil
}

func (r *sqliteNotificationRepo) ListByCheckID(ctx context.Context, checkID string, limit int) ([]*model.Notification, error) {
	const q = `
SELECT id, check_id, channel_id, type, sent_at, error
FROM notifications
WHERE check_id = ?
ORDER BY sent_at DESC, id DESC
LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, checkID, limit)
	if err != nil {
		return nil, fmt.Errorf("notificationRepo.ListByCheckID: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var notifications []*model.Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("notificationRepo.ListByCheckID scan: %w", err)
		}
		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notificationRepo.ListByCheckID rows: %w", err)
	}
	return notifications, nil
}
