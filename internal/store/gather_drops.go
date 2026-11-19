package store

import (
	"context"
	"database/sql"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
)

// AddGatherDrops adds a gathering run's filter counts to the paper's date.
func (s *Store) AddGatherDrops(ctx context.Context, scope auth.PaperScope, date string, counts map[string]int) error {
	paper, err := scopeID(scope)
	if err != nil {
		return err
	}
	if _, err = edition.ParseDate(date); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for rule, n := range counts {
		if _, err = tx.ExecContext(ctx, `INSERT INTO gather_drops(paper_id,date,rule,count) VALUES (?,?,?,?)
ON CONFLICT(paper_id,date,rule) DO UPDATE SET count=count+excluded.count`, paper, date, rule, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func qualityGatherDrops(ctx context.Context, tx *sql.Tx, paper, start, end string, data *QualityData) error {
	data.DroppedByRule = map[string]int{}
	rows, err := tx.QueryContext(ctx, `SELECT rule,sum(count) FROM gather_drops WHERE paper_id=? AND date>=? AND date<=? GROUP BY rule`, paper, start, end)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var rule string
		var n int
		if err = rows.Scan(&rule, &n); err != nil {
			return err
		}
		data.DroppedByRule[rule] = n
	}
	return rows.Err()
}
