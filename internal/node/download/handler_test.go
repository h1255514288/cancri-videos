package download

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/shared/auth"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
)

func TestHandler_InvalidMethod(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), "test-secret", slog.Default())
	req := httptest.NewRequest(http.MethodPost, "/d/token", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandler_InvalidPath(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), "test-secret", slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/d/", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandler_InvalidToken(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), "test-secret", slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/d/invalid-token", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandler_ClaimNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secret := "test-secret-key-at-least-32-chars-long"
	token, err := auth.SignDownloadToken("clm_test", "vid_test", 1, 1, time.Hour, secret)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE claim_id=\$1`).
		WithArgs("clm_test").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), secret, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/d/"+token, nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestHandler_ClaimExpired(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secret := "test-secret-key-at-least-32-chars-long"
	token, err := auth.SignDownloadToken("clm_test", "vid_test", 1, 1, time.Hour, secret)
	if err != nil {
		t.Fatal(err)
	}

	pastTime := time.Now().Add(-time.Hour)
	claimRows := sqlmock.NewRows([]string{"id", "claim_id", "user_id", "video_id", "idempotency_key", "status", "claimed_at", "claim_expires_at", "credential_version", "retry_count"}).
		AddRow(1, "clm_test", 1, 1, "idem-key", string(model.Reserved), time.Now(), pastTime, 1, 0)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE claim_id=\$1`).
		WithArgs("clm_test").
		WillReturnRows(claimRows)
	mock.ExpectRollback()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), secret, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/d/"+token, nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Errorf("expected 410, got %d", w.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestHandler_AlreadyConsumed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secret := "test-secret-key-at-least-32-chars-long"
	token, err := auth.SignDownloadToken("clm_test", "vid_test", 1, 1, time.Hour, secret)
	if err != nil {
		t.Fatal(err)
	}

	completedAt := time.Now()
	claimRows := sqlmock.NewRows([]string{"id", "claim_id", "user_id", "video_id", "idempotency_key", "status", "claimed_at", "claim_expires_at", "completed_at", "credential_version", "retry_count"}).
		AddRow(1, "clm_test", 1, 1, "idem-key", string(model.Completed), time.Now(), time.Now().Add(time.Hour), completedAt, 1, 1)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE claim_id=\$1`).
		WithArgs("clm_test").
		WillReturnRows(claimRows)
	mock.ExpectRollback()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), secret, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/d/"+token, nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusGone {
		t.Errorf("expected 410, got %d", w.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestHandler_VersionMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secret := "test-secret-key-at-least-32-chars-long"
	token, err := auth.SignDownloadToken("clm_test", "vid_test", 1, 1, time.Hour, secret)
	if err != nil {
		t.Fatal(err)
	}

	claimRows := sqlmock.NewRows([]string{"id", "claim_id", "user_id", "video_id", "idempotency_key", "status", "claimed_at", "claim_expires_at", "credential_version", "retry_count"}).
		AddRow(1, "clm_test", 1, 1, "idem-key", string(model.Reserved), time.Now(), time.Now().Add(time.Hour), 2, 0)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE claim_id=\$1`).
		WithArgs("clm_test").
		WillReturnRows(claimRows)
	mock.ExpectRollback()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), secret, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/d/"+token, nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestUpdateDownloadStatus_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	claimRows := sqlmock.NewRows([]string{"id", "claim_id", "user_id", "video_id", "idempotency_key", "status", "claimed_at", "claim_expires_at", "credential_version", "retry_count"}).
		AddRow(1, "clm_test", 1, 1, "idem-key", string(model.Streaming), time.Now(), time.Now().Add(time.Hour), 1, 1)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE id=\$1`).
		WithArgs(int64(1)).
		WillReturnRows(claimRows)
	mock.ExpectExec(`UPDATE claims SET status=\$1, completed_at=\$2 WHERE id=\$3`).
		WithArgs(string(model.Completed), sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE videos SET status='consumed', consumed_at=\$1 WHERE id=\$2`).
		WithArgs(sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), "test-secret", slog.Default())
	h.updateDownloadStatus(context.Background(), 1, "clm_test", true, 1024, 1)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestUpdateDownloadStatus_FailedRetryable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	claimRows := sqlmock.NewRows([]string{"id", "claim_id", "user_id", "video_id", "idempotency_key", "status", "claimed_at", "claim_expires_at", "credential_version", "retry_count", "retry_deadline"}).
		AddRow(1, "clm_test", 1, 1, "idem-key", string(model.Streaming), time.Now(), time.Now().Add(time.Hour), 1, 1, time.Now().Add(time.Hour))

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE id=\$1`).
		WithArgs(int64(1)).
		WillReturnRows(claimRows)
	mock.ExpectExec(`UPDATE claims SET status=\$1 WHERE id=\$2`).
		WithArgs(string(model.Retryable), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), "test-secret", slog.Default())
	h.updateDownloadStatus(context.Background(), 1, "clm_test", false, 512, 1)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestUpdateDownloadStatus_FailedMaxRetries(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	claimRows := sqlmock.NewRows([]string{"id", "claim_id", "user_id", "video_id", "idempotency_key", "status", "claimed_at", "claim_expires_at", "credential_version", "retry_count"}).
		AddRow(1, "clm_test", 1, 1, "idem-key", string(model.Streaming), time.Now(), time.Now().Add(time.Hour), 1, model.MaxAttempts)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE id=\$1`).
		WithArgs(int64(1)).
		WillReturnRows(claimRows)
	mock.ExpectExec(`UPDATE claims SET status=\$1, completed_at=\$2 WHERE id=\$3`).
		WithArgs(string(model.Deleting), sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), "test-secret", slog.Default())
	h.updateDownloadStatus(context.Background(), 1, "clm_test", false, 0, model.MaxAttempts)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestUpdateDownloadStatus_DBError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE id=\$1`).
		WithArgs(int64(1)).
		WillReturnError(errors.New("db error"))
	mock.ExpectRollback()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), "test-secret", slog.Default())
	h.updateDownloadStatus(context.Background(), 1, "clm_test", true, 1024, 1)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestHandler_FullDownload_Simulation(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test-video.mp4")
	testContent := []byte("fake video content for testing")
	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	secret := "test-secret-key-at-least-32-chars-long"
	token, err := auth.SignDownloadToken("clm_success", "vid_test", 5, 1, time.Hour, secret)
	if err != nil {
		t.Fatal(err)
	}

	claimExpiresAt := time.Now().Add(10 * time.Minute)
	claimRows := sqlmock.NewRows([]string{
		"id", "claim_id", "user_id", "video_id", "idempotency_key", "status",
		"claimed_at", "claim_expires_at", "first_download_at", "retry_deadline",
		"completed_at", "credential_version", "retry_count",
	}).AddRow(
		10, "clm_success", 1, 20, "idem-key", string(model.Reserved),
		time.Now(), claimExpiresAt, nil, nil, nil, 1, 0,
	)

	videoRows := sqlmock.NewRows([]string{
		"id", "video_id", "node_id", "filename", "storage_path", "size_bytes", "sha256", "mime_type",
	}).AddRow(
		20, "vid_test", int64(5), "test-video.mp4", "test-video.mp4", int64(len(testContent)), "abc123", sql.NullString{String: "video/mp4", Valid: true},
	)

	nodeRows := sqlmock.NewRows([]string{"storage_root_path"}).
		AddRow(tmpDir)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE claim_id=\$1`).
		WithArgs("clm_success").
		WillReturnRows(claimRows)
	mock.ExpectQuery(`SELECT id, video_id, node_id, filename, storage_path, size_bytes, sha256, mime_type FROM videos WHERE id=\$1`).
		WithArgs(int64(20)).
		WillReturnRows(videoRows)
	mock.ExpectQuery(`SELECT storage_root_path FROM storage_nodes WHERE id=\$1`).
		WithArgs(int64(5)).
		WillReturnRows(nodeRows)
	mock.ExpectExec(`UPDATE claims SET first_download_at=\$1, retry_deadline=\$2, retry_count=\$3, status=\$4 WHERE id=\$5`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), 1, string(model.Streaming), int64(10)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM claims WHERE id=\$1`).WithArgs(int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "claim_id", "video_id", "status", "retry_count"}).AddRow(10, "clm_success", 20, "streaming", 1))
	mock.ExpectExec(`UPDATE claims SET status=\$1, completed_at=\$2 WHERE id=\$3`).
		WithArgs("completed", sqlmock.AnyArg(), int64(10)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE videos SET status='consumed', consumed_at=\$1 WHERE id=\$2`).
		WithArgs(sqlmock.AnyArg(), int64(20)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	h := NewHandler(sqlx.NewDb(db, "sqlmock"), secret, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/d/"+token, nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if w.Header().Get("Content-Type") != "video/mp4" {
		t.Errorf("unexpected Content-Type: %s", w.Header().Get("Content-Type"))
	}

	if w.Body.String() != string(testContent) {
		t.Errorf("content mismatch: got %d bytes, expected %d", w.Body.Len(), len(testContent))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
