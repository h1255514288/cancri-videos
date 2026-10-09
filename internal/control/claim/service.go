package claim

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
)

var (
	ErrNoVideo             = errors.New("no_available_video")
	ErrQuotaExceeded       = errors.New("quota_exceeded")
	ErrIdempotencyConflict = errors.New("idempotency_conflict")
	ErrCategoryForbidden   = errors.New("category_forbidden")
	ErrPendingLimit        = errors.New("pending_claim_limit")
	ErrUserDisabled        = errors.New("user_disabled")
	ErrInvalidRequest      = errors.New("invalid_request")
)

const videoColumns = `id, video_id, category_id, node_id, COALESCE(title, '') AS title,
 filename, storage_path, size_bytes, sha256, COALESCE(mime_type, '') AS mime_type,
 status, uploaded_by, uploaded_at, claimed_at, first_download_at, consumed_at, deleted_at, error_message`

type Result struct {
	Claim        *model.ClaimRecord
	Video        *model.Video
	CategoryName string
	DailyLimit   int
	UsedToday    int
}

type userLimits struct {
	ID           int64  `db:"id"`
	Status       string `db:"status"`
	DailyLimit   int    `db:"daily_limit"`
	PendingLimit int    `db:"pending_limit"`
}

type Service struct {
	db *sqlx.DB
}

func NewService(database *sqlx.DB) *Service {
	return &Service{db: database}
}

func generateID(prefix string) string {
	b := make([]byte, 15)
	rand.Read(b)
	return prefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)[:20]
}

func (s *Service) Claim(ctx context.Context, userID int64, categoryID int64, idempotencyKey string) (*Result, error) {
	if userID <= 0 || categoryID <= 0 || strings.TrimSpace(idempotencyKey) == "" || len(idempotencyKey) > 128 {
		return nil, ErrInvalidRequest
	}
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var limits userLimits
	err = tx.GetContext(ctx, &limits,
		`SELECT u.id, u.status, COALESCE(u.daily_quota, g.default_daily_quota, 100) AS daily_limit,
   COALESCE(u.max_pending_claims, g.default_max_pending_claims, 3) AS pending_limit
   FROM users u LEFT JOIN user_groups g ON g.id=u.group_id WHERE u.id=$1 FOR UPDATE OF u`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserDisabled
	}
	if err != nil {
		return nil, fmt.Errorf("lock user: %w", err)
	}
	if limits.Status != "active" {
		return nil, ErrUserDisabled
	}

	var existing model.ClaimRecord
	err = tx.GetContext(ctx, &existing,
		`SELECT * FROM claims WHERE user_id=$1 AND idempotency_key=$2`, userID, idempotencyKey)
	replay := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check idempotency: %w", err)
	}
	var video model.Video
	if replay {
		if err := tx.GetContext(ctx, &video, `SELECT `+videoColumns+` FROM videos WHERE id=$1`, existing.VideoID); err != nil {
			return nil, fmt.Errorf("load video: %w", err)
		}
		if video.CategoryID != categoryID {
			return nil, ErrIdempotencyConflict
		}
	}

	var categoryName string
	err = tx.GetContext(ctx, &categoryName,
		`SELECT c.name FROM categories c JOIN user_category_permissions p ON p.category_id=c.id
   WHERE c.id=$1 AND c.status='enabled' AND p.user_id=$2 FOR SHARE OF c, p`, categoryID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCategoryForbidden
	}
	if err != nil {
		return nil, fmt.Errorf("check category permission: %w", err)
	}

	now := time.Now().UTC()
	today := now.Truncate(24 * time.Hour)
	if replay {
		var used int
		err := tx.GetContext(ctx, &used, `SELECT used_count FROM daily_quotas WHERE user_id=$1 AND quota_date=$2`, userID, today)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get quota: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit replay: %w", err)
		}
		return &Result{Claim: &existing, Video: &video, CategoryName: categoryName, DailyLimit: limits.DailyLimit, UsedToday: used}, nil
	}
	if limits.DailyLimit <= 0 {
		return nil, ErrQuotaExceeded
	}
	if limits.PendingLimit <= 0 {
		return nil, ErrPendingLimit
	}
	var pending int
	err = tx.GetContext(ctx, &pending,
		`SELECT COUNT(*) FROM claims WHERE user_id=$1 AND
   ((status='reserved' AND claim_expires_at>$2) OR status='streaming' OR
    (status='retryable' AND retry_deadline>$2 AND retry_count<$3))`, userID, now, model.MaxAttempts)
	if err != nil {
		return nil, fmt.Errorf("count pending claims: %w", err)
	}
	if pending >= limits.PendingLimit {
		return nil, ErrPendingLimit
	}

	var used int
	err = tx.GetContext(ctx, &used,
		`INSERT INTO daily_quotas (user_id, quota_date, used_count, last_claim_at)
   SELECT $1, $2, 1, $4 WHERE $3 > 0
   ON CONFLICT (user_id, quota_date)
   DO UPDATE SET used_count=daily_quotas.used_count+1, last_claim_at=EXCLUDED.last_claim_at
   WHERE daily_quotas.used_count < $3 RETURNING used_count`, userID, today, limits.DailyLimit, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrQuotaExceeded
	}
	if err != nil {
		return nil, fmt.Errorf("update quota: %w", err)
	}

	err = tx.GetContext(ctx, &video,
		`SELECT `+videoColumns+` FROM videos WHERE category_id=$1 AND status='available'
   ORDER BY RANDOM() LIMIT 1 FOR UPDATE SKIP LOCKED`, categoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoVideo
	}
	if err != nil {
		return nil, fmt.Errorf("select video: %w", err)
	}

	now = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE videos SET status='claimed', claimed_at=$1 WHERE id=$2`, now, video.ID)
	if err != nil {
		return nil, fmt.Errorf("update video: %w", err)
	}
	claimID := generateID("clm_")
	record := model.ClaimRecord{ClaimID: claimID, UserID: userID, VideoID: video.ID, IdempotencyKey: idempotencyKey,
		Status: string(model.Reserved), ClaimedAt: now, ClaimExpiresAt: now.Add(model.ReservationWindow), CredentialVersion: 1}
	err = tx.GetContext(ctx, &record.ID,
		`INSERT INTO claims (claim_id, user_id, video_id, idempotency_key, status, claimed_at, claim_expires_at, credential_version, retry_count)
   VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		record.ClaimID, record.UserID, record.VideoID, record.IdempotencyKey, record.Status,
		record.ClaimedAt, record.ClaimExpiresAt, record.CredentialVersion, record.RetryCount)
	if err != nil {
		return nil, fmt.Errorf("insert claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	video.Status = "claimed"
	video.ClaimedAt = &now
	return &Result{Claim: &record, Video: &video, CategoryName: categoryName, DailyLimit: limits.DailyLimit, UsedToday: used}, nil
}

func (s *Service) GetClaim(ctx context.Context, claimID string, userID int64) (*model.ClaimRecord, error) {
	var claim model.ClaimRecord
	err := s.db.GetContext(ctx, &claim,
		`SELECT * FROM claims WHERE claim_id=$1 AND user_id=$2`,
		claimID, userID)
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

func (s *Service) StartDownload(ctx context.Context, claimID string, attemptID string, nodeID int64) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var claim model.ClaimRecord
	err = tx.GetContext(ctx, &claim, `SELECT * FROM claims WHERE claim_id=$1 FOR UPDATE`, claimID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if claim.FirstDownloadAt == nil {
		claim.FirstDownloadAt = &now
		retryDeadline := now.Add(model.RetryWindow)
		claim.RetryDeadline = &retryDeadline
	}
	claim.RetryCount++
	claim.Status = string(model.Streaming)

	_, err = tx.ExecContext(ctx,
		`UPDATE claims SET first_download_at=$1, retry_deadline=$2, retry_count=$3, status=$4 WHERE id=$5`,
		claim.FirstDownloadAt, claim.RetryDeadline, claim.RetryCount, claim.Status, claim.ID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO download_attempts (attempt_id, claim_id, node_id, credential_version, started_at, bytes_sent)
   VALUES ($1, $2, $3, $4, $5, 0)`,
		attemptID, claim.ID, nodeID, claim.CredentialVersion, now)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `UPDATE videos SET status='downloading', first_download_at=$1 WHERE id=$2`, now, claim.VideoID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Service) FinishDownload(ctx context.Context, attemptID string, bytesSent int64, complete bool, errorMsg string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var result string
	if complete {
		result = "completed"
	} else if errorMsg != "" {
		result = "error"
	} else {
		result = "client_disconnect"
	}

	var attempt model.DownloadAttempt
	err = tx.GetContext(ctx, &attempt, `SELECT * FROM download_attempts WHERE attempt_id=$1`, attemptID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE download_attempts SET ended_at=$1, bytes_sent=$2, result=$3, error_message=$4 WHERE id=$5`,
		now, bytesSent, result, sql.NullString{String: errorMsg, Valid: errorMsg != ""}, attempt.ID)
	if err != nil {
		return err
	}

	var claim model.ClaimRecord
	err = tx.GetContext(ctx, &claim, `SELECT * FROM claims WHERE id=$1 FOR UPDATE`, attempt.ClaimID)
	if err != nil {
		return err
	}

	if complete {
		claim.Status = string(model.Completed)
		claim.CompletedAt = &now
		_, err = tx.ExecContext(ctx,
			`UPDATE claims SET status=$1, completed_at=$2 WHERE id=$3`,
			claim.Status, claim.CompletedAt, claim.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE videos SET status='consumed', consumed_at=$1 WHERE id=$2`, now, claim.VideoID)
	} else {
		newStatus := string(model.Retryable)
		if claim.RetryDeadline != nil && !now.Before(*claim.RetryDeadline) {
			newStatus = string(model.Deleting)
		}
		if claim.RetryCount >= model.MaxAttempts {
			newStatus = string(model.Deleting)
		}
		_, err = tx.ExecContext(ctx, `UPDATE claims SET status=$1 WHERE id=$2`, newStatus, claim.ID)
		if err != nil {
			return err
		}
		if newStatus == string(model.Deleting) {
			_, err = tx.ExecContext(ctx, `UPDATE videos SET status='pending_deletion' WHERE id=$1`, claim.VideoID)
		}
	}

	return tx.Commit()
}
