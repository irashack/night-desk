package store

import (
	"context"
	"database/sql"

	"github.com/irashack/night-desk/internal/auth"
	"github.com/irashack/night-desk/internal/edition"
)

// HostHealth counts one host's article fetches: outcomes by class, cool-offs and
// the extraction method that produced usable text.
type HostHealth struct {
	Host           string `json:"host"`
	OK             int    `json:"ok" minimum:"0" maximum:"100000"`
	FailedHTTP4xx  int    `json:"failed_http_4xx" minimum:"0" maximum:"100000"`
	FailedHTTP429  int    `json:"failed_http_429" minimum:"0" maximum:"100000"`
	FailedHTTP5xx  int    `json:"failed_http_5xx" minimum:"0" maximum:"100000"`
	FailedNetwork  int    `json:"failed_network" minimum:"0" maximum:"100000"`
	FailedTooLarge int    `json:"failed_too_large" minimum:"0" maximum:"100000"`
	CooledOff      int    `json:"cooled_off" minimum:"0" maximum:"100000"`
	Readability    int    `json:"readability" minimum:"0" maximum:"100000"`
	JSONLD         int    `json:"json_ld" minimum:"0" maximum:"100000"`
	AMP            int    `json:"amp" minimum:"0" maximum:"100000"`
}

// Failed is every failed fetch, not counting cool-offs.
func (h HostHealth) Failed() int {
	return h.FailedHTTP4xx + h.FailedHTTP429 + h.FailedHTTP5xx + h.FailedNetwork + h.FailedTooLarge
}

const healthColumns = "ok,failed_http_4xx,failed_http_429,failed_http_5xx,failed_network,failed_too_large,cooled_off,readability,json_ld,amp"

func (h *HostHealth) counts() []any {
	return []any{&h.OK, &h.FailedHTTP4xx, &h.FailedHTTP429, &h.FailedHTTP5xx, &h.FailedNetwork, &h.FailedTooLarge, &h.CooledOff, &h.Readability, &h.JSONLD, &h.AMP}
}

// AddFetchHealth adds a gathering run's per-host counts to the paper's date.
func (s *Store) AddFetchHealth(ctx context.Context, scope auth.PaperScope, date string, hosts []HostHealth) error {
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
	for _, h := range hosts {
		args := []any{paper, date, h.Host}
		for _, p := range h.counts() {
			args = append(args, *p.(*int))
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO fetch_health(paper_id,date,host,`+healthColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(paper_id,date,host) DO UPDATE SET ok=ok+excluded.ok,failed_http_4xx=failed_http_4xx+excluded.failed_http_4xx,
failed_http_429=failed_http_429+excluded.failed_http_429,failed_http_5xx=failed_http_5xx+excluded.failed_http_5xx,
failed_network=failed_network+excluded.failed_network,failed_too_large=failed_too_large+excluded.failed_too_large,
cooled_off=cooled_off+excluded.cooled_off,readability=readability+excluded.readability,json_ld=json_ld+excluded.json_ld,amp=amp+excluded.amp`, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// QualityHealthHosts bounds the hosts the quality report lists.
const QualityHealthHosts = 20

// qualityFetchHealth lists the window's hosts with the most failures and
// cool-offs, then the most fetches.
func qualityFetchHealth(ctx context.Context, tx *sql.Tx, paper, start, end string, data *QualityData) error {
	data.FetchHealth = []HostHealth{}
	rows, err := tx.QueryContext(ctx, `SELECT host,sum(ok),sum(failed_http_4xx),sum(failed_http_429),sum(failed_http_5xx),sum(failed_network),sum(failed_too_large),sum(cooled_off),sum(readability),sum(json_ld),sum(amp)
FROM fetch_health WHERE paper_id=? AND date>=? AND date<=? GROUP BY host
ORDER BY sum(failed_http_4xx+failed_http_429+failed_http_5xx+failed_network+failed_too_large+cooled_off) DESC, sum(ok) DESC, host LIMIT ?`, paper, start, end, QualityHealthHosts)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h HostHealth
		if err = rows.Scan(append([]any{&h.Host}, h.counts()...)...); err != nil {
			return err
		}
		data.FetchHealth = append(data.FetchHealth, h)
	}
	return rows.Err()
}
