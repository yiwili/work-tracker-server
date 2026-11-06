package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/yiwili/work-tracker-server/internal/config"
	"github.com/yiwili/work-tracker-server/internal/httpx"
)

var dummyBcryptHash string

func init() {
	h, _ := bcrypt.GenerateFromPassword([]byte("dummy-password"), bcrypt.MinCost)
	dummyBcryptHash = string(h)
}

type Service struct {
	pool   *pgxpool.Pool
	cfg    config.Config
	nowFn  func() time.Time
}

func NewService(pool *pgxpool.Pool, cfg config.Config) *Service {
	return &Service{
		pool:  pool,
		cfg:   cfg,
		nowFn: time.Now,
	}
}

func (s *Service) SetNow(fn func() time.Time) {
	s.nowFn = fn
}

func hashToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(h[:])
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "wts_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *Service) Register(ctx context.Context, email, password, displayName string) (Account, error) {
	if !s.cfg.AllowRegistration {
		return Account{}, httpx.E(http.StatusForbidden, "registration_disabled", "public registration is disabled")
	}

	normEmail := strings.ToLower(strings.TrimSpace(email))
	if len(normEmail) == 0 || len(normEmail) > 320 || !strings.Contains(normEmail, "@") {
		return Account{}, httpx.E(http.StatusBadRequest, "validation_failed", "invalid email address")
	}

	if len(password) < 8 || len(password) > 128 {
		return Account{}, httpx.E(http.StatusBadRequest, "validation_failed", "password length must be between 8 and 128 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cfg.BcryptCost)
	if err != nil {
		return Account{}, fmt.Errorf("hash password: %w", err)
	}

	accID := uuid.New()
	now := s.nowFn()

	row := s.pool.QueryRow(ctx, `
		INSERT INTO accounts (id, email, password_hash, display_name, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		RETURNING id, email, display_name, unified_publish_enabled, created_at
	`, accID, normEmail, string(hash), displayName, now)

	var acc Account
	if err := row.Scan(&acc.ID, &acc.Email, &acc.DisplayName, &acc.UnifiedPublishEnabled, &acc.CreatedAt); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Account{}, httpx.E(http.StatusConflict, "email_taken", "an account with this email already exists")
		}
		return Account{}, fmt.Errorf("insert account: %w", err)
	}

	return acc, nil
}

func (s *Service) Login(ctx context.Context, email, password string, devInput DeviceInput) (Session, error) {
	normEmail := strings.ToLower(strings.TrimSpace(email))

	var acc Account
	var passwordHash string

	err := s.pool.QueryRow(ctx, `
		SELECT id, email, password_hash, display_name, unified_publish_enabled, created_at
		FROM accounts
		WHERE lower(email) = $1
	`, normEmail).Scan(&acc.ID, &acc.Email, &passwordHash, &acc.DisplayName, &acc.UnifiedPublishEnabled, &acc.CreatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
			return Session{}, httpx.E(http.StatusUnauthorized, "unauthorized", "invalid email or password")
		}
		return Session{}, fmt.Errorf("query account: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return Session{}, httpx.E(http.StatusUnauthorized, "unauthorized", "invalid email or password")
	}

	// Validate device
	plat := devInput.Platform
	if plat != "desktop" && plat != "mobile" && plat != "web" {
		return Session{}, httpx.E(http.StatusBadRequest, "validation_failed", "platform must be desktop, mobile, or web")
	}
	devName := strings.TrimSpace(devInput.Name)
	if devName == "" || len(devName) > 100 {
		return Session{}, httpx.E(http.StatusBadRequest, "validation_failed", "device name must be between 1 and 100 characters")
	}

	deviceID := uuid.New()
	now := s.nowFn()

	_, err = s.pool.Exec(ctx, `
		INSERT INTO devices (id, account_id, name, platform, client_version, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $6)
	`, deviceID, acc.ID, devName, plat, devInput.ClientVersion, now)
	if err != nil {
		return Session{}, fmt.Errorf("insert device: %w", err)
	}

	rawToken, err := generateToken()
	if err != nil {
		return Session{}, fmt.Errorf("generate token: %w", err)
	}

	tokenHash := hashToken(rawToken)

	_, err = s.pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, account_id, device_id, created_at)
		VALUES ($1, $2, $3, $4)
	`, tokenHash, acc.ID, deviceID, now)
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}

	dev := Device{
		ID:            deviceID,
		Name:          devName,
		Platform:      plat,
		ClientVersion: devInput.ClientVersion,
		CreatedAt:     now,
		LastSeenAt:    &now,
		Current:       true,
	}

	return Session{
		Token:   rawToken,
		Account: acc,
		Device:  dev,
	}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (uuid.UUID, uuid.UUID, error) {
	if !strings.HasPrefix(rawToken, "wts_") {
		return uuid.Nil, uuid.Nil, httpx.E(http.StatusUnauthorized, "unauthorized", "invalid token format")
	}

	tokenHash := hashToken(rawToken)

	var accountID, deviceID uuid.UUID
	var sessionRevoked, deviceRevoked *time.Time
	var lastSeenAt *time.Time

	err := s.pool.QueryRow(ctx, `
		SELECT s.account_id, s.device_id, s.revoked_at, d.revoked_at, d.last_seen_at
		FROM sessions s
		JOIN devices d ON d.id = s.device_id
		WHERE s.token_hash = $1
	`, tokenHash).Scan(&accountID, &deviceID, &sessionRevoked, &deviceRevoked, &lastSeenAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, uuid.Nil, httpx.E(http.StatusUnauthorized, "unauthorized", "session not found")
		}
		return uuid.Nil, uuid.Nil, fmt.Errorf("authenticate session: %w", err)
	}

	if sessionRevoked != nil || deviceRevoked != nil {
		return uuid.Nil, uuid.Nil, httpx.E(http.StatusUnauthorized, "unauthorized", "session or device revoked")
	}

	// Update last_seen_at at most once per minute
	now := s.nowFn()
	if lastSeenAt == nil || now.Sub(*lastSeenAt) > 1*time.Minute {
		_, _ = s.pool.Exec(ctx, `
			UPDATE devices
			SET last_seen_at = $1
			WHERE id = $2 AND (last_seen_at IS NULL OR last_seen_at < $1 - interval '1 minute')
		`, now, deviceID)
	}

	return accountID, deviceID, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	tokenHash := hashToken(rawToken)
	now := s.nowFn()
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = $1
		WHERE token_hash = $2 AND revoked_at IS NULL
	`, now, tokenHash)
	return err
}

func (s *Service) Me(ctx context.Context, accountID, deviceID uuid.UUID) (Account, Device, error) {
	var acc Account
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, display_name, unified_publish_enabled, created_at
		FROM accounts
		WHERE id = $1
	`, accountID).Scan(&acc.ID, &acc.Email, &acc.DisplayName, &acc.UnifiedPublishEnabled, &acc.CreatedAt)
	if err != nil {
		return Account{}, Device{}, err
	}

	var dev Device
	err = s.pool.QueryRow(ctx, `
		SELECT id, name, platform, client_version, created_at, last_seen_at, last_sync_at, revoked_at
		FROM devices
		WHERE id = $1 AND account_id = $2
	`, deviceID, accountID).Scan(&dev.ID, &dev.Name, &dev.Platform, &dev.ClientVersion, &dev.CreatedAt, &dev.LastSeenAt, &dev.LastSyncAt, &dev.RevokedAt)
	if err != nil {
		return Account{}, Device{}, err
	}
	dev.Current = true

	return acc, dev, nil
}

func (s *Service) ListDevices(ctx context.Context, accountID, currentDeviceID uuid.UUID) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, platform, client_version, created_at, last_seen_at, last_sync_at, revoked_at
		FROM devices
		WHERE account_id = $1
		ORDER BY created_at DESC
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Name, &d.Platform, &d.ClientVersion, &d.CreatedAt, &d.LastSeenAt, &d.LastSyncAt, &d.RevokedAt); err != nil {
			return nil, err
		}
		d.Current = (d.ID == currentDeviceID)
		devices = append(devices, d)
	}

	return devices, nil
}

func (s *Service) RevokeDevice(ctx context.Context, accountID, deviceID uuid.UUID) error {
	now := s.nowFn()

	// Check device belongs to account
	var exists bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM devices WHERE id=$1 AND account_id=$2)", deviceID, accountID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return httpx.E(http.StatusNotFound, "not_found", "device not found")
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE devices
		SET revoked_at = $1
		WHERE id = $2 AND account_id = $3 AND revoked_at IS NULL
	`, now, deviceID, accountID)
	if err != nil {
		return err
	}

	// Also revoke all sessions belonging to this device
	_, err = s.pool.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = $1
		WHERE device_id = $2 AND revoked_at IS NULL
	`, now, deviceID)

	return err
}

func (s *Service) TouchSync(ctx context.Context, deviceID uuid.UUID) error {
	now := s.nowFn()
	_, err := s.pool.Exec(ctx, `
		UPDATE devices
		SET last_sync_at = $1
		WHERE id = $2
	`, now, deviceID)
	return err
}
