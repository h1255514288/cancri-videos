package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
)

type CleanupService struct {
	db     *sqlx.DB
	logger *slog.Logger
	nodeID int64
	storageRoot string
}

func NewCleanupService(database *sqlx.DB, logger *slog.Logger) *CleanupService {
	if logger == nil { logger = slog.Default() }
	return &CleanupService{
		db:     database,
		logger: logger,
	}
}

func NewNodeCleanupService(database *sqlx.DB, logger *slog.Logger, nodeID int64, root string) *CleanupService {
	s := NewCleanupService(database, logger)
	s.nodeID, s.storageRoot = nodeID, root
	return s
}

// ReleaseExpiredClaims releases videos from expired reserved claims
func (s *CleanupService) ReleaseExpiredClaims(ctx context.Context) (int, error) {
	now := time.Now().UTC()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil { return 0, err }
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE claims 
		SET status = $1, completed_at = $3, credential_version = credential_version + 1
		WHERE status = $2 AND first_download_at IS NULL
		AND claim_expires_at <= $3
	`, string(model.Completed), string(model.Reserved), now)

	if err != nil {
		return 0, fmt.Errorf("update expired claims: %w", err)
	}

	rows, _ := result.RowsAffected()
	
	if rows > 0 {
		// Release videos back to available
		_, err = tx.ExecContext(ctx, `
			UPDATE videos v
			SET status = 'available', claimed_at = NULL
			WHERE status = 'claimed'
			AND EXISTS (SELECT 1 FROM claims expired WHERE expired.video_id=v.id
			 AND expired.status='completed' AND expired.first_download_at IS NULL AND expired.claim_expires_at <= $4)
			AND NOT EXISTS (
				SELECT 1 FROM claims c 
				WHERE c.video_id = v.id 
				AND c.status IN ($1, $2, $3)
			)
		`, string(model.Reserved), string(model.Streaming), string(model.Retryable), now)
		
		if err != nil {
			return int(rows), fmt.Errorf("release videos: %w", err)
		}
	}

	if err := tx.Commit(); err != nil { return 0, err }
	s.logger.Info("released expired claims", "count", rows)
	return int(rows), nil
}

// CleanupRetryableExpired marks retryable claims as completed if retry window expired
func (s *CleanupService) CleanupRetryableExpired(ctx context.Context) (int, error) {
	now := time.Now().UTC()

	result, err := s.db.ExecContext(ctx, `
		UPDATE claims
		SET status = $1, completed_at = $2
		WHERE status = $3
		AND (retry_deadline <= $2 OR retry_count >= $4)
	`, string(model.Deleting), now, string(model.Retryable), model.MaxAttempts)

	if err != nil {
		return 0, fmt.Errorf("cleanup retryable: %w", err)
	}

	rows, _ := result.RowsAffected()
	s.logger.Info("cleaned up expired retryable claims", "count", rows)
	return int(rows), nil
}

// MarkVideosForDeletion marks consumed videos as deleting
func (s *CleanupService) MarkVideosForDeletion(ctx context.Context) (int, error) {
	now := time.Now().UTC()

	// Mark videos from completed claims as deleting
	result, err := s.db.ExecContext(ctx, `
		UPDATE videos v
		SET status = 'pending_deletion'
		WHERE status IN ('consumed', 'claimed')
		AND EXISTS (
			SELECT 1 FROM claims c
			WHERE c.video_id = v.id
			AND c.status IN ($1, 'deleting') AND c.first_download_at IS NOT NULL
			AND c.completed_at < $2
		) AND NOT EXISTS (SELECT 1 FROM claims active WHERE active.video_id=v.id
		 AND active.status IN ('reserved', 'streaming', 'retryable'))
	`, string(model.Completed), now.Add(-1*time.Hour))

	if err != nil {
		return 0, fmt.Errorf("mark videos for deletion: %w", err)
	}

	rows, _ := result.RowsAffected()
	s.logger.Info("marked videos for deletion", "count", rows)
	return int(rows), nil
}

// ProcessDeletionJobs processes pending deletion jobs
func (s *CleanupService) ProcessDeletionJobs(ctx context.Context, maxJobs int) (int, error) {
	if s.nodeID <= 0 || s.storageRoot == "" { return 0, nil }
	tx, err := s.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Get pending deletion jobs
	var jobs []struct {
		ID          int64  `db:"id"`
		VideoID     int64  `db:"video_id"`
		StoragePath string `db:"storage_path"`
		NodeID      int64  `db:"node_id"`
	}

	err = tx.SelectContext(ctx, &jobs, `
		SELECT dj.id, dj.video_id, v.storage_path, v.node_id
		FROM deletion_jobs dj
		JOIN videos v ON v.id = dj.video_id
		WHERE dj.status = 'pending'
		AND dj.retry_count < dj.max_retries
		AND dj.next_attempt_at <= NOW() AND dj.node_id = $2
		AND v.status = 'pending_deletion'
		ORDER BY dj.created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, maxJobs, s.nodeID)

	if err != nil {
		return 0, fmt.Errorf("select jobs: %w", err)
	}

	if len(jobs) == 0 {
		return 0, nil
	}

	processed := 0
	for _, job := range jobs {
		path, removeErr := deletionPath(s.storageRoot, job.StoragePath)
		if removeErr == nil {
			removeErr = os.Remove(path)
			if errors.Is(removeErr, os.ErrNotExist) { removeErr = nil }
		}
		if removeErr != nil {
			_, err = tx.ExecContext(ctx, `UPDATE deletion_jobs SET retry_count=retry_count+1,
			 last_error=$1, next_attempt_at=NOW()+INTERVAL '1 minute',
			 status=CASE WHEN retry_count+1>=max_retries THEN 'failed' ELSE 'pending' END WHERE id=$2`, removeErr.Error(), job.ID)
			if err != nil { return 0, err }
			continue
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE deletion_jobs
			SET status = 'completed', completed_at = $1
			WHERE id = $2
		`, time.Now().UTC(), job.ID)

		if err != nil {
			return 0, fmt.Errorf("complete deletion job: %w", err)
		}

		// Mark video as deleted
		_, err = tx.ExecContext(ctx, `
			UPDATE videos
			SET status = 'deleted', deleted_at = $1
			WHERE id = $2
		`, time.Now().UTC(), job.VideoID)

		if err != nil {
			return 0, fmt.Errorf("mark video deleted: %w", err)
		}

		processed++
	}

	if err := tx.Commit(); err != nil {
		return processed, fmt.Errorf("commit: %w", err)
	}

	s.logger.Info("processed deletion jobs", "processed", processed, "total", len(jobs))
	return processed, nil
}

// CreateDeletionJobs creates deletion jobs for videos marked as deleting
func (s *CleanupService) CreateDeletionJobs(ctx context.Context) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO deletion_jobs (video_id, node_id, storage_path, status, retry_count, created_at)
		SELECT id, node_id, storage_path, 'pending', 0, NOW()
		FROM videos
		WHERE status = 'pending_deletion'
		AND NOT EXISTS (
			SELECT 1 FROM deletion_jobs dj
			WHERE dj.video_id = videos.id
			AND dj.status IN ('pending', 'in_progress', 'completed', 'failed')
		)
	`)

	if err != nil {
		return 0, fmt.Errorf("create deletion jobs: %w", err)
	}

	rows, _ := result.RowsAffected()
	s.logger.Info("created deletion jobs", "count", rows)
	return int(rows), nil
}

// CleanupTask runs all cleanup tasks periodically
func (s *CleanupService) CleanupTask(ctx context.Context) error {
	s.logger.Info("starting cleanup task")
	var failures []error

	// Release expired reserved claims
	released, err := s.ReleaseExpiredClaims(ctx)
	if err != nil {
		failures = append(failures, err)
		s.logger.Error("release expired claims failed", "error", err)
	}

	// Cleanup expired retryable claims
	cleaned, err := s.CleanupRetryableExpired(ctx)
	if err != nil {
		failures = append(failures, err)
		s.logger.Error("cleanup retryable expired failed", "error", err)
	}

	// Mark videos for deletion
	marked, err := s.MarkVideosForDeletion(ctx)
	if err != nil {
		failures = append(failures, err)
		s.logger.Error("mark videos for deletion failed", "error", err)
	}

	// Create deletion jobs
	created, err := s.CreateDeletionJobs(ctx)
	if err != nil {
		failures = append(failures, err)
		s.logger.Error("create deletion jobs failed", "error", err)
	}

	// Process deletion jobs
	processed, err := s.ProcessDeletionJobs(ctx, 100)
	if err != nil {
		failures = append(failures, err)
		s.logger.Error("process deletion jobs failed", "error", err)
	}

	s.logger.Info("cleanup task completed",
		"released_claims", released,
		"cleaned_retryable", cleaned,
		"marked_videos", marked,
		"created_jobs", created,
		"processed_jobs", processed,
	)

	return errors.Join(failures...)
}

// RunPeriodic runs cleanup task periodically
func (s *CleanupService) RunPeriodic(ctx context.Context, interval time.Duration) error {
	if interval <= 0 { return errors.New("cleanup interval must be positive") }
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on start
	if err := s.CleanupTask(ctx); err != nil {
		s.logger.Error("initial cleanup task failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("cleanup service stopping")
			return ctx.Err()
		case <-ticker.C:
			if err := s.CleanupTask(ctx); err != nil {
				s.logger.Error("cleanup task failed", "error", err)
			}
		}
	}
}

// RetryFailedJobs retries failed deletion jobs
func (s *CleanupService) RetryFailedJobs(ctx context.Context) (int, error) {
	now := time.Now().UTC()

	result, err := s.db.ExecContext(ctx, `
		UPDATE deletion_jobs
		SET status = 'pending', retry_count = retry_count + 1
		WHERE status = 'failed'
		AND retry_count < 5
		AND created_at > $1
	`, now.Add(-24*time.Hour))

	if err != nil {
		return 0, fmt.Errorf("retry failed jobs: %w", err)
	}

	rows, _ := result.RowsAffected()
	s.logger.Info("retried failed deletion jobs", "count", rows)
	return int(rows), nil
}

// GetStats returns cleanup service statistics
func deletionPath(root, name string) (string, error) {
	if !filepath.IsLocal(name) { return "", errors.New("invalid deletion path") }
	base, err := filepath.EvalSymlinks(root)
	if err != nil { return "", err }
	base, err = filepath.Abs(base)
	if err != nil { return "", err }
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Join(base, name)))
	if err != nil { return "", err }
	rel, err := filepath.Rel(base, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("deletion path escapes storage root")
	}
	return filepath.Join(parent, filepath.Base(name)), nil
}

func (s *CleanupService) GetStats(ctx context.Context) (map[string]int, error) {
	stats := make(map[string]int)

	// Count expired reserved claims
	var expiredReserved int
	err := s.db.GetContext(ctx, &expiredReserved, `
		SELECT COUNT(*) FROM claims
		WHERE status = $1 AND claim_expires_at < NOW()
	`, string(model.Reserved))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("count expired reserved: %w", err)
	}
	stats["expired_reserved"] = expiredReserved

	// Count expired retryable claims
	var expiredRetryable int
	err = s.db.GetContext(ctx, &expiredRetryable, `
		SELECT COUNT(*) FROM claims
		WHERE status = $1 AND retry_deadline < NOW()
	`, string(model.Retryable))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("count expired retryable: %w", err)
	}
	stats["expired_retryable"] = expiredRetryable

	// Count pending deletion jobs
	var pendingJobs int
	err = s.db.GetContext(ctx, &pendingJobs, `
		SELECT COUNT(*) FROM deletion_jobs WHERE status = 'pending'
	`)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("count pending jobs: %w", err)
	}
	stats["pending_deletion_jobs"] = pendingJobs

	// Count videos marked for deletion
	var deletingVideos int
	err = s.db.GetContext(ctx, &deletingVideos, `
		SELECT COUNT(*) FROM videos WHERE status = 'pending_deletion'
	`)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("count deleting videos: %w", err)
	}
	stats["deleting_videos"] = deletingVideos

	return stats, nil
}
