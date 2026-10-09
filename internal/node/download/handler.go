package download

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/shared/auth"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
)

type ClaimStatus = model.ClaimState

const (
	Reserved  = model.Reserved
	Streaming = model.Streaming
	Retryable = model.Retryable
	Completed = model.Completed
	Deleting  = model.Deleting
)

var (
	ErrInvalidToken   = errors.New("invalid_token")
	ErrClaimNotFound  = errors.New("claim_not_found")
	ErrClaimExpired   = errors.New("claim_expired")
	ErrVideoNotFound  = errors.New("video_not_found")
	ErrAlreadyConsumed = errors.New("already_consumed")
	ErrRetryExceeded  = errors.New("retry_exceeded")
	ErrDownloadActive = errors.New("download_already_active")
)

type Handler struct {
	db        *sqlx.DB
	jwtSecret string
	logger    *slog.Logger
	nodeID int64
}

func NewHandler(database *sqlx.DB, jwtSecret string, logger *slog.Logger, nodeIDs ...int64) *Handler {
	if logger == nil { logger = slog.Default() }
	var nodeID int64
	if len(nodeIDs)>0 { nodeID=nodeIDs[0] }
	return &Handler{
		nodeID: nodeID,
		db:        database,
		jwtSecret: jwtSecret,
		logger:    logger,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/d/")
	if path == "" || path == r.URL.Path {
		http.Error(w, "invalid download path", http.StatusBadRequest)
		return
	}

	token := path
	claims, err := auth.VerifyDownloadToken(token, h.jwtSecret)
	if err != nil {
		h.logger.Warn("token verification failed", "error", err)
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}

	if h.nodeID > 0 && claims.NodeID != h.nodeID {
		http.Error(w, "wrong storage node", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	if err := h.handleDownload(ctx, w, r, claims); err != nil {
		h.logger.Error("download failed", "claim_id", claims.ClaimID, "error", err)
		switch {
		case errors.Is(err, ErrInvalidToken):
			http.Error(w, "invalid token", http.StatusUnauthorized)
		case errors.Is(err, ErrDownloadActive):
			http.Error(w, "download already active", http.StatusConflict)
		case errors.Is(err, ErrClaimNotFound), errors.Is(err, ErrVideoNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, ErrClaimExpired), errors.Is(err, ErrAlreadyConsumed), errors.Is(err, ErrRetryExceeded):
			http.Error(w, "download no longer available", http.StatusGone)
		default:
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}
}

func (h *Handler) handleDownload(ctx context.Context, w http.ResponseWriter, r *http.Request, claims *auth.DownloadClaims) error {
	tx, err := h.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var claim model.ClaimRecord
	err = tx.GetContext(ctx, &claim, `SELECT * FROM claims WHERE claim_id=$1 FOR UPDATE`, claims.ClaimID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClaimNotFound
	}
	if err != nil {
		return fmt.Errorf("load claim: %w", err)
	}

	now := time.Now().UTC()
	status := ClaimStatus(claim.Status)

	switch status {
	case Reserved:
		if !now.Before(claim.ClaimExpiresAt) {
			return ErrClaimExpired
		}
	case Retryable:
		if claim.RetryDeadline == nil || !now.Before(*claim.RetryDeadline) {
			return ErrClaimExpired
		}
		if claim.RetryCount >= model.MaxAttempts {
			return ErrRetryExceeded
		}
	case Completed, Deleting:
		return ErrAlreadyConsumed
	case Streaming:
		if h.nodeID <= 0 { return ErrDownloadActive }
		if claim.RetryDeadline == nil || !now.Before(*claim.RetryDeadline) { return ErrClaimExpired }
		if claim.RetryCount >= model.MaxAttempts { return ErrRetryExceeded }
	default:
		return fmt.Errorf("unexpected claim status: %s", status)
	}

	if claim.CredentialVersion != claims.CredentialVersion {
		return ErrInvalidToken
	}

	var video struct {
		ID          int64  `db:"id"`
		VideoID     string `db:"video_id"`
		NodeID      int64  `db:"node_id"`
		Filename    string `db:"filename"`
		StoragePath string `db:"storage_path"`
		SizeBytes   int64  `db:"size_bytes"`
		SHA256      string `db:"sha256"`
		MimeType    sql.NullString `db:"mime_type"`
	}
	err = tx.GetContext(ctx, &video, `SELECT id, video_id, node_id, filename, storage_path, size_bytes, sha256, mime_type FROM videos WHERE id=$1`, claim.VideoID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVideoNotFound
	}
	if err != nil {
		return fmt.Errorf("load video: %w", err)
	}

	if video.NodeID != claims.NodeID || video.VideoID != claims.VideoID {
		return ErrInvalidToken
	}

	var storageRoot string
	err = tx.GetContext(ctx, &storageRoot, `SELECT storage_root_path FROM storage_nodes WHERE id=$1`, video.NodeID)
	if err != nil {
		return fmt.Errorf("load node: %w", err)
	}

	unlock, err := acquireClaimLock(storageRoot, claim.ClaimID)
	if err != nil { return err }
	defer func() {
		if err := unlock(); err != nil { h.logger.Error("release download lock failed", "claim_id", claim.ClaimID, "error", err) }
	}()
	fullPath, err := safeFilePath(storageRoot, video.StoragePath)
	if err != nil { return err }
	file, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}
	if !stat.Mode().IsRegular() || stat.Size()!=video.SizeBytes { return errors.New("invalid stored file") }

	if status == Reserved || status == Retryable || status == Streaming {
		firstDownload := claim.FirstDownloadAt == nil
		if firstDownload {
			claim.FirstDownloadAt = &now
			retryDeadline := now.Add(model.RetryWindow)
			claim.RetryDeadline = &retryDeadline
		}
		claim.RetryCount++
		claim.Status = string(Streaming)

		_, err = tx.ExecContext(ctx, `UPDATE claims SET first_download_at=$1, retry_deadline=$2, retry_count=$3, status=$4 WHERE id=$5`,
			claim.FirstDownloadAt, claim.RetryDeadline, claim.RetryCount, claim.Status, claim.ID)
		if err != nil {
			return fmt.Errorf("update claim: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	contentType := "application/octet-stream"
	if video.MimeType.Valid && video.MimeType.String != "" {
		contentType = video.MimeType.String
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, video.Filename))
	w.Header().Set("X-Content-SHA256", video.SHA256)
	w.Header().Set("Accept-Ranges", "none")

	w.WriteHeader(http.StatusOK)

	written, err := io.Copy(w, file)
	success := err == nil && written == stat.Size()

	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h.updateDownloadStatus(finishCtx, claim.ID, claim.ClaimID, success, written, claim.RetryCount)

	return nil
}

func (h *Handler) updateDownloadStatus(ctx context.Context, claimID int64, claimPublicID string, success bool, bytesWritten int64, expectedAttempt int) {
	if expectedAttempt <= 0 {
		h.logger.Error("invalid download attempt", "claim_id", claimPublicID)
		return
	}
	tx, err := h.db.BeginTxx(ctx, nil)
	if err != nil {
		h.logger.Error("update status: begin tx failed", "claim_id", claimPublicID, "error", err)
		return
	}
	defer tx.Rollback()

	var claim model.ClaimRecord
	err = tx.GetContext(ctx, &claim, `SELECT * FROM claims WHERE id=$1 FOR UPDATE`, claimID)
	if err != nil {
		h.logger.Error("update status: load claim failed", "claim_id", claimPublicID, "error", err)
		return
	}

	if claim.Status != string(Streaming) || claim.RetryCount != expectedAttempt {
		h.logger.Error("refusing stale download completion", "claim_id", claimPublicID)
		return
	}
	now := time.Now().UTC()
	if success {
		claim.Status = string(Completed)
		claim.CompletedAt = &now
		_, err = tx.ExecContext(ctx, `UPDATE claims SET status=$1, completed_at=$2 WHERE id=$3`,
			claim.Status, claim.CompletedAt, claim.ID)
		if err != nil {
			h.logger.Error("update status: mark completed failed", "claim_id", claimPublicID, "error", err)
			return
		}
		_, err = tx.ExecContext(ctx, `UPDATE videos SET status='consumed', consumed_at=$1 WHERE id=$2`, now, claim.VideoID)
		if err != nil {
			h.logger.Error("update status: mark video consumed failed", "claim_id", claimPublicID, "error", err)
			return
		}
	} else {
		if claim.RetryCount >= model.MaxAttempts || claim.RetryDeadline == nil || !now.Before(*claim.RetryDeadline) {
			claim.Status = string(Deleting)
			claim.CompletedAt = &now
			_, err = tx.ExecContext(ctx, `UPDATE claims SET status=$1, completed_at=$2 WHERE id=$3`,
				claim.Status, claim.CompletedAt, claim.ID)
		} else {
			claim.Status = string(Retryable)
			_, err = tx.ExecContext(ctx, `UPDATE claims SET status=$1 WHERE id=$2`, claim.Status, claim.ID)
		}
		if err != nil {
			h.logger.Error("update status: mark retryable/completed failed", "claim_id", claimPublicID, "error", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("update status: commit failed", "claim_id", claimPublicID, "error", err)
		return
	}

	h.logger.Info("download finished", "claim_id", claimPublicID, "success", success, "bytes", bytesWritten)
}
