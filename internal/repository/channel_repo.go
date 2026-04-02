package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/myrrolinz/cronmon/internal/model"
)

// ChannelRepository defines read/write operations for Channel records and the
// many-to-many association between checks and channels.
type ChannelRepository interface {
	Create(ctx context.Context, ch *model.Channel) error
	GetByID(ctx context.Context, id int64) (*model.Channel, error)
	ListAll(ctx context.Context) ([]*model.Channel, error)
	Delete(ctx context.Context, id int64) error
	ListByCheckID(ctx context.Context, checkID string) ([]*model.Channel, error)
	AttachToCheck(ctx context.Context, checkID string, channelID int64) error
	DetachFromCheck(ctx context.Context, checkID string, channelID int64) error
}

type sqliteChannelRepo struct {
	db *sql.DB
}

// NewChannelRepository returns a SQLite-backed ChannelRepository.
func NewChannelRepository(db *sql.DB) ChannelRepository {
	return &sqliteChannelRepo{db: db}
}

func scanChannel(s rowScanner) (*model.Channel, error) {
	var (
		ch         model.Channel
		configStr  string
		createdStr string
	)
	err := s.Scan(&ch.ID, &ch.Type, &ch.Name, &configStr, &createdStr)
	if err != nil {
		return nil, err
	}
	ch.Config = []byte(configStr)
	ch.CreatedAt, err = parseTime(createdStr)
	if err != nil {
		return nil, fmt.Errorf("created_at: %w", err)
	}
	return &ch, nil
}

func (r *sqliteChannelRepo) Create(ctx context.Context, ch *model.Channel) error {
	const q = `
INSERT INTO channels (type, name, config, created_at)
VALUES (?, ?, ?, ?)`

	res, err := r.db.ExecContext(ctx, q,
		ch.Type, ch.Name, string(ch.Config), formatTime(ch.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("channelRepo.Create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("channelRepo.Create last id: %w", err)
	}
	ch.ID = id
	return nil
}

func (r *sqliteChannelRepo) GetByID(ctx context.Context, id int64) (*model.Channel, error) {
	const q = `SELECT id, type, name, config, created_at FROM channels WHERE id = ?`
	ch, err := scanChannel(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("channelRepo.GetByID %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("channelRepo.GetByID: %w", err)
	}
	return ch, nil
}

func (r *sqliteChannelRepo) ListAll(ctx context.Context) ([]*model.Channel, error) {
	const q = `SELECT id, type, name, config, created_at FROM channels ORDER BY created_at ASC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("channelRepo.ListAll: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var channels []*model.Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("channelRepo.ListAll scan: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("channelRepo.ListAll rows: %w", err)
	}
	return channels, nil
}

func (r *sqliteChannelRepo) Delete(ctx context.Context, id int64) error {
	const q = `DELETE FROM channels WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("channelRepo.Delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("channelRepo.Delete rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("channelRepo.Delete %d: %w", id, ErrNotFound)
	}
	return nil
}

func (r *sqliteChannelRepo) ListByCheckID(ctx context.Context, checkID string) ([]*model.Channel, error) {
	const q = `
SELECT c.id, c.type, c.name, c.config, c.created_at
FROM channels c
JOIN check_channels cc ON c.id = cc.channel_id
WHERE cc.check_id = ?
ORDER BY c.created_at ASC`

	rows, err := r.db.QueryContext(ctx, q, checkID)
	if err != nil {
		return nil, fmt.Errorf("channelRepo.ListByCheckID: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var channels []*model.Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("channelRepo.ListByCheckID scan: %w", err)
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("channelRepo.ListByCheckID rows: %w", err)
	}
	return channels, nil
}

// AttachToCheck creates a check_channels row linking checkID and channelID.
// Uses INSERT OR IGNORE so the call is idempotent.
func (r *sqliteChannelRepo) AttachToCheck(ctx context.Context, checkID string, channelID int64) error {
	const q = `INSERT OR IGNORE INTO check_channels (check_id, channel_id) VALUES (?, ?)`
	_, err := r.db.ExecContext(ctx, q, checkID, channelID)
	if err != nil {
		return fmt.Errorf("channelRepo.AttachToCheck: %w", err)
	}
	return nil
}

// DetachFromCheck removes the check_channels row linking checkID and channelID.
// It is not an error if the association does not exist.
func (r *sqliteChannelRepo) DetachFromCheck(ctx context.Context, checkID string, channelID int64) error {
	const q = `DELETE FROM check_channels WHERE check_id = ? AND channel_id = ?`
	_, err := r.db.ExecContext(ctx, q, checkID, channelID)
	if err != nil {
		return fmt.Errorf("channelRepo.DetachFromCheck: %w", err)
	}
	return nil
}
