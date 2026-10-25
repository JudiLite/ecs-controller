package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var backupTables = []string{"settings", "accounts", "logs", "traffic_hourly", "traffic_daily", "billing_cache", "instance_traffic_usage", "passkey_credentials", "audit_logs", "rotation_states"}

func (s *Store) Snapshot(path string) error {
	if path == "" {
		return fmt.Errorf("snapshot path is empty")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove old snapshot: %w", err)
	}
	if _, err := s.DB.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("create database snapshot: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("protect database snapshot: %w", err)
	}
	return nil
}

func (s *Store) backupValue(value any, source *Store) (any, error) {
	text, ok := value.(string)
	if !ok {
		if n, ok := value.(json.Number); ok {
			return string(n), nil
		}
		return value, nil
	}
	if strings.HasPrefix(text, "ENC1") {
		plain, err := source.OpenSecret(text)
		if err != nil {
			return nil, err
		}
		return s.Seal(plain)
	}
	if !strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[") {
		return text, nil
	}
	var nested any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&nested) != nil {
		return text, nil
	}
	var visit func(any) (any, error)
	visit = func(v any) (any, error) {
		switch v := v.(type) {
		case string:
			return s.backupValue(v, source)
		case []any:
			for i, item := range v {
				next, err := visit(item)
				if err != nil {
					return nil, err
				}
				v[i] = next
			}
			return v, nil
		case map[string]any:
			for k, item := range v {
				next, err := visit(item)
				if err != nil {
					return nil, err
				}
				v[k] = next
			}
			return v, nil
		default:
			return v, nil
		}
	}
	next, err := visit(nested)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(next)
	return string(raw), err
}

func (s *Store) RestoreSnapshot(path string, encryptionKey [32]byte) error {
	s.Operations.Lock()
	defer s.Operations.Unlock()
	if path == "" {
		return fmt.Errorf("snapshot path is empty")
	}
	source, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return err
	}
	source.SetMaxOpenConns(1)
	defer source.Close()
	var integrity string
	if err := source.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("invalid restore snapshot")
	}
	available := map[string]bool{}
	for _, table := range backupTables {
		var count int
		if err := source.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			return err
		}
		if count != 1 && table != "audit_logs" && table != "rotation_states" {
			return fmt.Errorf("unsupported restore snapshot: table %s is missing", table)
		}
		available[table] = count == 1
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	sourceSecrets := &Store{Key: encryptionKey}
	for _, table := range backupTables {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
		if !available[table] {
			continue
		}
		targetRows, err := tx.Query("SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for targetRows.Next() {
			var column string
			if err := targetRows.Scan(&column); err != nil {
				targetRows.Close()
				return err
			}
			allowed[column] = true
		}
		targetErr := targetRows.Err()
		targetRows.Close()
		if targetErr != nil {
			return targetErr
		}
		rows, err := source.Query("SELECT * FROM " + table)
		if err != nil {
			return err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		quoted := make([]string, len(columns))
		placeholders := make([]string, len(columns))
		for index, column := range columns {
			if !allowed[column] {
				rows.Close()
				return fmt.Errorf("unsupported snapshot column %s.%s", table, column)
			}
			quoted[index] = "\"" + column + "\""
			placeholders[index] = "?"
		}
		statement := "INSERT INTO " + table + " (" + strings.Join(quoted, ",") + ") VALUES (" + strings.Join(placeholders, ",") + ")"
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return err
			}
			for index, column := range columns {
				if raw, ok := values[index].([]byte); ok {
					values[index] = string(raw)
				}
				if (table == "accounts" && column == "access_key_secret") || (table == "settings" && column == "value") {
					values[index], err = s.backupValue(values[index], sourceSecrets)
					if err != nil {
						rows.Close()
						return fmt.Errorf("restore credentials: %w", err)
					}
				}
			}
			if _, err := tx.Exec(statement, values...); err != nil {
				rows.Close()
				return err
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if rowErr != nil {
			return rowErr
		}
	}
	for _, table := range []string{"sessions", "login_attempts", "passkey_challenges", "telegram_action_tokens", "jobs", "ecs_create_tasks", "telegram_bot_state", "runtime_status"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("UPDATE accounts SET instance_status='ReleaseFailed',is_deleted=0 WHERE instance_status='Releasing'"); err != nil {
		return err
	}
	return tx.Commit()
}

func BackupTables() []string { return append([]string(nil), backupTables...) }
