package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/myrrolinz/cronmon/internal/model"
)

// PingRepository defines read/write operations for Ping records.
type PingRepository interface {
	Create(ctx context.Context, p *model.Ping) error
	ListByCheckID(ctx context.Context, checkID string, limit int) ([]*model.Ping, error)
	DeleteOldest(ctx context.Context, checkID string, keepN int) error
}

type sqlitePingRepo struct {
	db *sql.DB
}

// NewPingRepository returns a SQLite-backed PingRepository.
func NewPingRepository(db *sql.DB) PingRepository {
	return &sqlitePingRepo{db: db}
}

func scanPing(s rowScanner) (*model.Ping, error) {
	var (
		p          model.Ping
		typeStr    string
		createdStr string
	)
	err := s.Scan(&p.ID, &p.CheckID, &typeStr, &createdStr, &p.SourceIP)
	if err != nil {
		return nil, err
	}
	p.Type = model.PingType(typeStr)
	p.CreatedAt, err = parseTime(createdStr)
	if err != nil {
		return nil, fmt.Errorf("created_at: %w", err)
	}
	return &p, nil
}

func (r *sqlitePingRepo) Create(ctx context.Context, p *model.Ping) error {
	const q = `
INSERT INTO pings (check_id, type, created_at, source_ip)
VALUES (?, ?, ?, ?)`

	res, err := r.db.ExecContext(ctx, q,
		p.CheckID, string(p.Type), formatTime(p.CreatedAt), p.SourceIP,
	)
	if err != nil {
		return fmt.Errorf("pingRepo.Create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("pingRepo.Create last id: %w", err)
	}
	p.ID = id
	return nil
}

func (r *sqlitePingRepo) ListByCheckID(ctx context.Context, checkID string, limit int) ([]*model.Ping, error) {
	const q = `
SELECT id, check_id, type, created_at, source_ip
FROM pings
WHERE check_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, checkID, limit)
	if err != nil {
		return nil, fmt.Errorf("pingRepo.ListByCheckID: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var pings []*model.Ping
	for rows.Next() {
		p, err := scanPing(rows)
		if err != nil {
			return nil, fmt.Errorf("pingRepo.ListByCheckID scan: %w", err)
		}
		pings = append(pings, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pingRepo.ListByCheckID rows: %w", err)
	}
	return pings, nil
}

// DeleteOldest removes pings for the given check beyond the most recent keepN
// rows (ordered by created_at DESC). This is the per-check ping-retention
// cleanup called by the scheduler's hourly ticker.
func (r *sqlitePingRepo) DeleteOldest(ctx context.Context, checkID string, keepN int) error {
	const q = `
DELETE FROM pings
WHERE check_id = ?
  AND id NOT IN (
      SELECT id FROM pings
      WHERE check_id = ?
	      ORDER BY created_at DESC, id DESC
      LIMIT ?
  )`

	_, err := r.db.ExecContext(ctx, q, checkID, checkID, keepN)
	if err != nil {
		return fmt.Errorf("pingRepo.DeleteOldest: %w", err)
	}
	return nil
}
