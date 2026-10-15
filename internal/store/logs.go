package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidLogQuery = errors.New("invalid log query")

type LogQuery struct {
	Tab      string
	Level    string
	Search   string
	BeforeID int64
	Limit    int
}

type LogRecord struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"created_at"`
	Time      string `json:"time_str"`
}

type LogPage struct {
	Data          []LogRecord `json:"data"`
	HasMore       bool        `json:"has_more"`
	NextBeforeID  int64       `json:"next_before_id"`
	LatestID      int64       `json:"latest_id"`
	LastHeartbeat *LogRecord  `json:"last_heartbeat"`
}

func (s *Store) QueryLogs(ctx context.Context, query LogQuery) (LogPage, error) {
	page := LogPage{Data: []LogRecord{}}
	if query.Tab == "" {
		query.Tab = "action"
	}
	if query.Level == "" {
		query.Level = "all"
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	query.Search = strings.TrimSpace(query.Search)
	if query.Tab != "action" && query.Tab != "heartbeat" {
		return page, fmt.Errorf("%w: 无效的日志类别", ErrInvalidLogQuery)
	}
	if query.BeforeID < 0 || query.Limit < 1 || query.Limit > 100 || utf8.RuneCountInString(query.Search) > 200 {
		return page, fmt.Errorf("%w: 日志查询参数超出范围", ErrInvalidLogQuery)
	}
	if query.Level != "all" && (query.Tab == "heartbeat" || (query.Level != "info" && query.Level != "warning" && query.Level != "error")) {
		return page, fmt.Errorf("%w: 无效的日志级别", ErrInvalidLogQuery)
	}
	where := `type IN ('info','warning','error')`
	args := []any{}
	if query.Tab == "heartbeat" {
		where = `type = 'heartbeat'`
	} else if query.Level != "all" {
		where += ` AND type = ?`
		args = append(args, query.Level)
	}
	if query.Search != "" {
		where += ` AND message LIKE ? ESCAPE '\'`
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query.Search)
		args = append(args, "%"+escaped+"%")
	}

	// A read transaction gives the page and its watermarks one consistent view.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	pageWhere := where
	pageArgs := append([]any{}, args...)
	if query.BeforeID > 0 {
		pageWhere += ` AND id < ?`
		pageArgs = append(pageArgs, query.BeforeID)
	}
	pageArgs = append(pageArgs, query.Limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT id,type,message,created_at FROM logs WHERE `+pageWhere+` ORDER BY id DESC LIMIT ?`, pageArgs...)
	if err != nil {
		return page, err
	}
	for rows.Next() {
		var record LogRecord
		if err := rows.Scan(&record.ID, &record.Type, &record.Message, &record.CreatedAt); err != nil {
			rows.Close()
			return page, err
		}
		record.Time = time.Unix(record.CreatedAt, 0).Format("2006-01-02 15:04:05")
		page.Data = append(page.Data, record)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return page, err
	}
	if closeErr != nil {
		return page, closeErr
	}
	if len(page.Data) > query.Limit {
		page.HasMore = true
		page.Data = page.Data[:query.Limit]
	}
	if len(page.Data) > 0 {
		page.NextBeforeID = page.Data[len(page.Data)-1].ID
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM logs WHERE `+where+` ORDER BY id DESC LIMIT 1`, args...).Scan(&page.LatestID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return page, err
	}
	var heartbeat LogRecord
	err = tx.QueryRowContext(ctx, `SELECT id,type,message,created_at FROM logs WHERE type='heartbeat' ORDER BY id DESC LIMIT 1`).Scan(&heartbeat.ID, &heartbeat.Type, &heartbeat.Message, &heartbeat.CreatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return page, err
	}
	if err == nil {
		heartbeat.Time = time.Unix(heartbeat.CreatedAt, 0).Format("2006-01-02 15:04:05")
		page.LastHeartbeat = &heartbeat
	}
	if err := tx.Commit(); err != nil {
		return page, err
	}
	return page, nil
}
