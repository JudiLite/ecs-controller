package store

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JudiLite/ecs-controller/internal/app"
	"golang.org/x/crypto/pbkdf2"
)

const MaxBackupBytes = 32 << 20
const backupMagic = "ECSBACKUP1\n"

// Only durable application data is portable. Sessions and cloud operation queues
// must never be replayed on a different installation.
var backupTables = []string{"settings", "accounts", "logs", "audit_logs", "traffic_hourly", "traffic_daily", "billing_cache", "instance_traffic_usage", "rotation_states"}
var localSettings = map[string]bool{"admin_username": true, "admin_password": true, "password_login_enabled": true}
var backupSecretSettings = map[string]bool{"notify_password": true, "notify_tg_token": true, "notify_tg_proxy_pass": true, "account_group_secrets": true, "rotation_group_secrets": true}

type BackupTable struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type Backup struct {
	Format    int                    `json:"format"`
	Version   string                 `json:"version"`
	CreatedAt time.Time              `json:"created_at"`
	Key       [32]byte               `json:"key"`
	Tables    map[string]BackupTable `json:"tables"`
}

type BackupSummary struct {
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Accounts  int       `json:"accounts"`
	Instances int       `json:"instances"`
	Logs      int       `json:"logs"`
}

func (b *Backup) Summary() BackupSummary {
	var groups []json.RawMessage
	for _, row := range b.Tables["settings"].Rows {
		if len(row) == 2 && row[0] == "account_groups" {
			if text, ok := row[1].(string); ok {
				_ = json.Unmarshal([]byte(text), &groups)
			}
		}
	}
	return BackupSummary{Version: b.Version, CreatedAt: b.CreatedAt, Accounts: len(groups), Instances: len(b.Tables["accounts"].Rows), Logs: len(b.Tables["logs"].Rows) + len(b.Tables["audit_logs"].Rows)}
}

func (s *Store) CreateBackup(ctx context.Context, password string) ([]byte, error) {
	if utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return nil, errors.New("备份密码至少需要 12 个字符")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	b := Backup{Format: 1, Version: app.Version, CreatedAt: time.Now().UTC(), Key: s.Key, Tables: map[string]BackupTable{}}
	totalBytes := 0
	for _, name := range backupTables {
		rows, err := tx.QueryContext(ctx, `SELECT * FROM "`+name+`"`)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		table := BackupTable{Columns: columns, Rows: [][]any{}}
		for rows.Next() {
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, err
			}
			for i, value := range values {
				if raw, ok := value.([]byte); ok {
					values[i] = string(raw)
				}
			}
			if name == "settings" && localSettings[fmt.Sprint(values[0])] {
				continue
			}
			if name == "settings" && values[0] == "app_logo_url" && strings.Contains(fmt.Sprint(values[1]), "action=brand_logo") {
				values[1] = ""
				for _, ext := range []string{"png", "jpg", "webp"} {
					logo, readErr := os.ReadFile(filepath.Join(s.dataDir, "brand-logo."+ext))
					if readErr == nil && len(logo) <= 2<<20 {
						typ := http.DetectContentType(logo)
						if typ == "image/png" || typ == "image/jpeg" || typ == "image/webp" {
							values[1] = "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(logo)
							break
						}
					}
				}
			}
			encodedRow, marshalErr := json.Marshal(values)
			if marshalErr != nil {
				rows.Close()
				return nil, marshalErr
			}
			totalBytes += len(encodedRow)
			if totalBytes > MaxBackupBytes-65536 {
				rows.Close()
				return nil, errors.New("备份数据超过 32 MB，请先清理历史日志")
			}
			table.Rows = append(table.Rows, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		b.Tables[name] = table
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBackupBytes-128 {
		return nil, errors.New("备份数据超过 32 MB，请先清理历史日志")
	}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(pbkdf2.Key([]byte(password), salt, 600000, 32, sha256.New))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := append(append([]byte(backupMagic), salt...), nonce...)
	return append(header, gcm.Seal(nil, nonce, raw, header)...), nil
}

func DecodeBackup(raw []byte, password string) (*Backup, error) {
	headerLen := len(backupMagic) + 16 + 12
	if len(raw) > MaxBackupBytes || len(raw) < headerLen+16 || !bytes.HasPrefix(raw, []byte(backupMagic)) {
		return nil, errors.New("不是有效的 ECS 加密备份文件")
	}
	if utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return nil, errors.New("请输入备份时设置的密码（至少 12 个字符）")
	}
	salt, nonce := raw[len(backupMagic):len(backupMagic)+16], raw[len(backupMagic)+16:headerLen]
	block, err := aes.NewCipher(pbkdf2.Key([]byte(password), salt, 600000, 32, sha256.New))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, raw[headerLen:], raw[:headerLen])
	if err != nil {
		return nil, errors.New("备份密码错误，或文件已损坏")
	}
	var b Backup
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()
	if err := decoder.Decode(&b); err != nil {
		return nil, errors.New("备份内容无效")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("备份包含多余内容")
	}
	if b.Format != 1 || b.CreatedAt.IsZero() || len(b.Tables) != len(backupTables) {
		return nil, errors.New("备份格式不受支持")
	}
	for _, name := range backupTables {
		table, ok := b.Tables[name]
		if !ok || len(table.Columns) == 0 {
			return nil, fmt.Errorf("备份缺少数据表 %s", name)
		}
		for _, row := range table.Rows {
			if len(row) != len(table.Columns) {
				return nil, errors.New("备份数据不完整")
			}
			for _, value := range row {
				switch value.(type) {
				case nil, string, json.Number:
				default:
					return nil, errors.New("备份包含不支持的数据类型")
				}
			}
		}
	}
	return &b, nil
}

// Re-seal secrets with this installation's key, including encrypted values nested
// inside JSON settings. The local encryption key and administrator stay intact.
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

func (s *Store) restoreValue(name string, columns []string, row []any, index int, source *Store) (any, error) {
	if name == "accounts" && columns[index] == "access_key_secret" || name == "settings" && index == 1 && backupSecretSettings[fmt.Sprint(row[0])] {
		return s.backupValue(row[index], source)
	}
	if n, ok := row[index].(json.Number); ok {
		return string(n), nil
	}
	return row[index], nil
}

func checkBackupColumns(ctx context.Context, tx *sql.Tx, name string, table BackupTable) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM "`+name+`" LIMIT 0`)
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(columns) != len(table.Columns) {
		return nil, fmt.Errorf("数据表 %s 与当前版本不兼容", name)
	}
	for i, column := range columns {
		if column != table.Columns[i] {
			return nil, fmt.Errorf("数据表 %s 与当前版本不兼容", name)
		}
	}
	return columns, nil
}

func (s *Store) ValidateBackup(ctx context.Context, b *Backup) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source := &Store{Key: b.Key}
	for _, name := range backupTables {
		table := b.Tables[name]
		columns, err := checkBackupColumns(ctx, tx, name, table)
		if err != nil {
			return err
		}
		for _, row := range table.Rows {
			for i := range row {
				if _, err := s.restoreValue(name, columns, row, i, source); err != nil {
					return errors.New("备份中的凭据无法解密")
				}
			}
		}
	}
	return nil
}

// Caller holds Operations exclusively so no in-flight cloud workflow can write
// old account state after the restore transaction commits.
func (s *Store) RestoreBackup(ctx context.Context, b *Backup) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status IN ('queued','running','retry')`).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return errors.New("仍有待执行或重试中的后台任务，请完成后再恢复")
	}
	source := &Store{Key: b.Key}
	for _, name := range backupTables {
		table := b.Tables[name]
		columns, err := checkBackupColumns(ctx, tx, name, table)
		if err != nil {
			return err
		}
		query := `DELETE FROM "` + name + `"`
		if name == "settings" {
			query += ` WHERE key NOT IN ('admin_username','admin_password','password_login_enabled')`
		}
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return err
		}
		quoted := make([]string, len(columns))
		for i, column := range columns {
			quoted[i] = `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
		}
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO "`+name+`" (`+strings.Join(quoted, ",")+`) VALUES (`+strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")+`)`)
		if err != nil {
			return err
		}
		for _, row := range table.Rows {
			if name == "settings" && localSettings[fmt.Sprint(row[0])] {
				continue
			}
			values := make([]any, len(row))
			for i := range row {
				values[i], err = s.restoreValue(name, columns, row, i, source)
				if err != nil {
					stmt.Close()
					return fmt.Errorf("备份凭据无法解密: %w", err)
				}
			}
			if _, err = stmt.ExecContext(ctx, values...); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()
	}
	for _, name := range []string{"jobs", "ecs_create_tasks", "runtime_status", "telegram_action_tokens", "telegram_bot_state", "passkey_challenges", "sessions"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM "`+name+`"`); err != nil {
			return err
		}
	}
	// Old archive URLs must not point at unrelated logo files on this server.
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key='app_logo_url' AND value LIKE '%action=brand_logo%'`); err != nil {
		return err
	}
	return tx.Commit()
}
