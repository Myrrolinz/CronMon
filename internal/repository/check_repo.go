package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/myrrolinz/cronmon/internal/model"
)

// CheckRepository defines read/write operations for Check records.
type CheckRepository interface {
	Create(ctx context.Context, c *model.Check) error
	GetByID(ctx context.Context, id string) (*model.Check, error)
	GetByUUID(ctx context.Context, uuid string) (*model.Check, error)
	ListAll(ctx context.Context) ([]*model.Check, error)
	Update(ctx context.Context, c *model.Check) error
	Delete(ctx context.Context, id string) error
}

type sqliteCheckRepo struct {
	db *sql.DB
}

// NewCheckRepository returns a SQLite-backed CheckRepository.
func NewCheckRepository(db *sql.DB) CheckRepository {
	return &sqliteCheckRepo{db: db}
}

const checkSelectCols = `id, name, slug, schedule, grace, status,
	last_ping_at, next_expected_at, created_at, updated_at, tags, notify_on_fail`

func scanCheck(s rowScanner) (*model.Check, error) {
	var (
		c            model.Check
		statusStr    string
		slugNS       sql.NullString
		lastPingNS   sql.NullString
		nextExpNS    sql.NullString
		createdStr   string
		updatedStr   string
		notifyOnFail int
	)
	err := s.Scan(
		&c.ID, &c.Name, &slugNS, &c.Schedule, &c.Grace,
		&statusStr, &lastPingNS, &nextExpNS,
		&createdStr, &updatedStr, &c.Tags, &notifyOnFail,
	)
	if err != nil {
		return nil, err
	}

	c.Status = model.Status(statusStr)
	c.NotifyOnFail = notifyOnFail != 0

	if slugNS.Valid {
		c.Slug = &slugNS.String
	}

	c.CreatedAt, err = parseTime(createdStr)
	if err != nil {
		return nil, fmt.Errorf("created_at: %w", err)
	}
	c.UpdatedAt, err = parseTime(updatedStr)
	if err != nil {
		return nil, fmt.Errorf("updated_at: %w", err)
	}
	c.LastPingAt, err = parseTimePtr(lastPingNS)
	if err != nil {
		return nil, fmt.Errorf("last_ping_at: %w", err)
	}
	c.NextExpectedAt, err = parseTimePtr(nextExpNS)
	if err != nil {
		return nil, fmt.Errorf("next_expected_at: %w", err)
	}

	return &c, nil
}

func (r *sqliteCheckRepo) Create(ctx context.Context, c *model.Check) error {
	const q = `
INSERT INTO checks
	(id, name, slug, schedule, grace, status, last_ping_at, next_expected_at, created_at, updated_at, tags, notify_on_fail)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	notifyOnFail := 0
	if c.NotifyOnFail {
		notifyOnFail = 1
	}
	_, err := r.db.ExecContext(ctx, q,
		c.ID, c.Name, stringPtrToNull(c.Slug),
		c.Schedule, c.Grace, string(c.Status),
		formatTimePtr(c.LastPingAt), formatTimePtr(c.NextExpectedAt),
		formatTime(c.CreatedAt), formatTime(c.UpdatedAt),
		c.Tags, notifyOnFail,
	)
	if err != nil {
		return fmt.Errorf("checkRepo.Create: %w", err)
	}
	return nil
}

func (r *sqliteCheckRepo) GetByID(ctx context.Context, id string) (*model.Check, error) {
	const q = `SELECT ` + checkSelectCols + ` FROM checks WHERE id = ?`
	c, err := scanCheck(r.db.QueryRowContext(ctx, q, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("checkRepo.GetByID %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("checkRepo.GetByID: %w", err)
	}
	return c, nil
}

// GetByUUID looks up a Check by its UUID. In CronMon the primary key IS the
// UUID, so this is semantically equivalent to GetByID but used by the ping
// handler path for clarity.
func (r *sqliteCheckRepo) GetByUUID(ctx context.Context, uuid string) (*model.Check, error) {
	const q = `SELECT ` + checkSelectCols + ` FROM checks WHERE id = ?`
	c, err := scanCheck(r.db.QueryRowContext(ctx, q, uuid))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("checkRepo.GetByUUID %q: %w", uuid, ErrNotFound)
		}
		return nil, fmt.Errorf("checkRepo.GetByUUID: %w", err)
	}
	return c, nil
}

func (r *sqliteCheckRepo) ListAll(ctx context.Context) ([]*model.Check, error) {
	const q = `SELECT ` + checkSelectCols + ` FROM checks ORDER BY created_at ASC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("checkRepo.ListAll: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var checks []*model.Check
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("checkRepo.ListAll scan: %w", err)
		}
		checks = append(checks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("checkRepo.ListAll rows: %w", err)
	}
	return checks, nil
}

func (r *sqliteCheckRepo) Update(ctx context.Context, c *model.Check) error {
	const q = `
UPDATE checks SET
	name = ?, slug = ?, schedule = ?, grace = ?, status = ?,
	last_ping_at = ?, next_expected_at = ?, updated_at = ?, tags = ?, notify_on_fail = ?
WHERE id = ?`

	notifyOnFail := 0
	if c.NotifyOnFail {
		notifyOnFail = 1
	}
	res, err := r.db.ExecContext(ctx, q,
		c.Name, stringPtrToNull(c.Slug), c.Schedule, c.Grace, string(c.Status),
		formatTimePtr(c.LastPingAt), formatTimePtr(c.NextExpectedAt),
		formatTime(c.UpdatedAt), c.Tags, notifyOnFail,
		c.ID,
	)
	if err != nil {
		return fmt.Errorf("checkRepo.Update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checkRepo.Update rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("checkRepo.Update %q: %w", c.ID, ErrNotFound)
	}
	return nil
}

func (r *sqliteCheckRepo) Delete(ctx context.Context, id string) error {
	const q = `DELETE FROM checks WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, id)
	if err != nil {
		return fmt.Errorf("checkRepo.Delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("checkRepo.Delete rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("checkRepo.Delete %q: %w", id, ErrNotFound)
	}
	return nil
}
