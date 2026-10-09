package testutil

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func MockDB(t *testing.T) (*sqlx.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return sqlx.NewDb(db, "sqlmock"), mock
}

func ClaimRows(now time.Time, state string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "claim_id", "user_id", "video_id", "idempotency_key", "status", "claimed_at", "claim_expires_at", "credential_version", "retry_count"}).
		AddRow(11, "clm_existing", 7, 21, "same-key", state, now, now.Add(10*time.Minute), 1, 0)
}

func VideoRows(categoryID int64) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "video_id", "category_id", "node_id", "title", "filename", "size_bytes", "sha256", "status"}).
		AddRow(21, "vid_test", categoryID, 2, "Test video", "test.mp4", 100, "test-sha", "available")
}

func ExpectUserLock(mock sqlmock.Sqlmock, daily, pending int) {
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT u.id, u.status, COALESCE.*FOR UPDATE OF u").WithArgs(7).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "daily_limit", "pending_limit"}).AddRow(7, "active", daily, pending))
}

func ExpectNoClaim(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT \\* FROM claims WHERE user_id=\\$1 AND idempotency_key=\\$2").WithArgs(7, "same-key").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
}

func ExpectPermission(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("SELECT c.name.*FOR SHARE OF c, p").WithArgs(3, 7).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("test-category"))
}

func ExpectPending(mock sqlmock.Sqlmock, count int) {
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM claims.*retry_count<\\$3").WithArgs(7, sqlmock.AnyArg(), 6).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func ExpectQuota(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("INSERT INTO daily_quotas.*WHERE \\$3 > 0.*RETURNING used_count").WithArgs(7, sqlmock.AnyArg(), 100, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"used_count"}).AddRow(1))
}

func ExpectClaimSuccess(mock sqlmock.Sqlmock) {
	ExpectUserLock(mock, 100, 3)
	ExpectNoClaim(mock)
	ExpectPermission(mock)
	ExpectPending(mock, 0)
	ExpectQuota(mock)
	mock.ExpectQuery("SELECT .* FROM videos WHERE category_id=\\$1.*FOR UPDATE SKIP LOCKED").WithArgs(3).WillReturnRows(VideoRows(3))
	mock.ExpectExec("UPDATE videos SET status='claimed'").WithArgs(sqlmock.AnyArg(), 21).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO claims.*RETURNING id").
		WithArgs(sqlmock.AnyArg(), 7, 21, "same-key", "reserved", sqlmock.AnyArg(), sqlmock.AnyArg(), 1, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
	mock.ExpectCommit()
}
