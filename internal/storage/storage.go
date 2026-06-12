package storage

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var ErrCodeInvalid = errors.New("code invalid or expired")

type Code struct {
	ID        string
	Label     string
	ExpiresAt time.Time
	CreatedAt time.Time
	UsedAt    *time.Time
	RevokedAt *time.Time
}

type Device struct {
	ID          string
	Fingerprint string
	MacID       string
	Name        string
	CreatedAt   time.Time
	LastSeenAt  time.Time
	RevokedAt   *time.Time
}

type DB struct {
	db           *sql.DB
	pepper       string
	deviceSecret string
}

func Open(path, pepper, deviceSecret string) (*DB, error) {
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &DB{db: db, pepper: pepper, deviceSecret: deviceSecret}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS invite_codes (
			id TEXT PRIMARY KEY,
			code_hash TEXT UNIQUE NOT NULL,
			label TEXT DEFAULT '',
			expires_at INTEGER NOT NULL,
			used_at INTEGER,
			revoked_at INTEGER,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS devices (
			id TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			mac_id TEXT UNIQUE NOT NULL,
			name TEXT DEFAULT '',
			token_hash TEXT NOT NULL,
			revoked_at INTEGER,
			created_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id TEXT PRIMARY KEY,
			event_type TEXT NOT NULL,
			subject_id TEXT DEFAULT '',
			ip TEXT DEFAULT '',
			user_agent TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			metadata_json TEXT DEFAULT '{}'
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomUpperAlpha(n int) (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b), nil
}

func hmacHex(data, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

func (d *DB) CreateCode(label string, ttl time.Duration) (id, plaintext string, err error) {
	id, err = randomHex(16)
	if err != nil {
		return "", "", err
	}
	plaintext, err = randomUpperAlpha(8)
	if err != nil {
		return "", "", err
	}
	codeHash := hmacHex(plaintext, d.pepper)
	now := time.Now()
	expiresAt := now.Add(ttl)
	_, err = d.db.Exec(
		`INSERT INTO invite_codes (id, code_hash, label, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, codeHash, label, expiresAt.Unix(), now.Unix(),
	)
	if err != nil {
		return "", "", fmt.Errorf("insert code: %w", err)
	}
	return id, plaintext, nil
}

func (d *DB) ListCodes() ([]Code, error) {
	rows, err := d.db.Query(
		`SELECT id, label, expires_at, created_at, used_at, revoked_at FROM invite_codes ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []Code
	for rows.Next() {
		var c Code
		var expiresAt, createdAt int64
		var usedAt, revokedAt sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Label, &expiresAt, &createdAt, &usedAt, &revokedAt); err != nil {
			return nil, err
		}
		c.ExpiresAt = time.Unix(expiresAt, 0)
		c.CreatedAt = time.Unix(createdAt, 0)
		if usedAt.Valid {
			t := time.Unix(usedAt.Int64, 0)
			c.UsedAt = &t
		}
		if revokedAt.Valid {
			t := time.Unix(revokedAt.Int64, 0)
			c.RevokedAt = &t
		}
		codes = append(codes, c)
	}
	return codes, rows.Err()
}

func (d *DB) RevokeCode(id string) error {
	res, err := d.db.Exec(
		`UPDATE invite_codes SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().Unix(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("code not found or already revoked")
	}
	return nil
}

func (d *DB) RedeemCode(plaintext, fingerprint, deviceName string) (*Device, string, error) {
	codeHash := hmacHex(plaintext, d.pepper)
	now := time.Now()

	tx, err := d.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()

	var codeID string
	var expiresAt int64
	var usedAt, revokedAt sql.NullInt64
	err = tx.QueryRow(
		`SELECT id, expires_at, used_at, revoked_at FROM invite_codes WHERE code_hash = ?`,
		codeHash,
	).Scan(&codeID, &expiresAt, &usedAt, &revokedAt)
	if err == sql.ErrNoRows {
		return nil, "", ErrCodeInvalid
	}
	if err != nil {
		return nil, "", err
	}
	if usedAt.Valid || revokedAt.Valid || time.Unix(expiresAt, 0).Before(now) {
		return nil, "", ErrCodeInvalid
	}

	if _, err = tx.Exec(`UPDATE invite_codes SET used_at = ? WHERE id = ?`, now.Unix(), codeID); err != nil {
		return nil, "", err
	}

	deviceID, err := randomHex(16)
	if err != nil {
		return nil, "", err
	}

	var macID string
	for {
		macID, err = randomHex(6)
		if err != nil {
			return nil, "", err
		}
		var exists int
		if scanErr := tx.QueryRow(`SELECT COUNT(*) FROM devices WHERE mac_id = ?`, macID).Scan(&exists); scanErr != nil {
			return nil, "", scanErr
		}
		if exists == 0 {
			break
		}
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, "", err
	}
	deviceToken := hex.EncodeToString(tokenBytes)
	tokenHash := hmacHex(deviceToken, d.deviceSecret)

	_, err = tx.Exec(
		`INSERT INTO devices (id, fingerprint, mac_id, name, token_hash, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		deviceID, fingerprint, macID, deviceName, tokenHash, now.Unix(), now.Unix(),
	)
	if err != nil {
		return nil, "", fmt.Errorf("insert device: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, "", err
	}

	_ = d.LogAudit("code_redeemed", codeID, "", "", map[string]any{"device_id": deviceID, "fingerprint": fingerprint})

	dev := &Device{
		ID:          deviceID,
		Fingerprint: fingerprint,
		MacID:       macID,
		Name:        deviceName,
		CreatedAt:   now,
		LastSeenAt:  now,
	}
	return dev, deviceToken, nil
}

func (d *DB) GetDeviceByTokenHash(hash string) (*Device, error) {
	var dev Device
	var createdAt, lastSeenAt int64
	var revokedAt sql.NullInt64
	err := d.db.QueryRow(
		`SELECT id, fingerprint, mac_id, name, created_at, last_seen_at, revoked_at
		 FROM devices WHERE token_hash = ? AND revoked_at IS NULL`,
		hash,
	).Scan(&dev.ID, &dev.Fingerprint, &dev.MacID, &dev.Name, &createdAt, &lastSeenAt, &revokedAt)
	if err == sql.ErrNoRows {
		return nil, errors.New("device not found")
	}
	if err != nil {
		return nil, err
	}
	dev.CreatedAt = time.Unix(createdAt, 0)
	dev.LastSeenAt = time.Unix(lastSeenAt, 0)
	if revokedAt.Valid {
		t := time.Unix(revokedAt.Int64, 0)
		dev.RevokedAt = &t
	}
	return &dev, nil
}

func (d *DB) ValidateToken(token string) (*Device, error) {
	hash := hmacHex(token, d.deviceSecret)
	return d.GetDeviceByTokenHash(hash)
}

func (d *DB) UpdateLastSeen(deviceID string) error {
	_, err := d.db.Exec(`UPDATE devices SET last_seen_at = ? WHERE id = ?`, time.Now().Unix(), deviceID)
	return err
}

func (d *DB) RevokeDevice(id string) error {
	res, err := d.db.Exec(
		`UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		time.Now().Unix(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("device not found or already revoked")
	}
	return nil
}

func (d *DB) LogAudit(eventType, subjectID, ip, ua string, meta map[string]any) error {
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	metaJSON := "{}"
	if meta != nil {
		if b, err2 := json.Marshal(meta); err2 == nil {
			metaJSON = string(b)
		}
	}
	_, err = d.db.Exec(
		`INSERT INTO audit_events (id, event_type, subject_id, ip, user_agent, created_at, metadata_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, eventType, subjectID, ip, ua, time.Now().Unix(), metaJSON,
	)
	return err
}
