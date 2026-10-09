package claim

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yourorg/video-distribution-go/internal/shared/testutil"
)

func TestClaimSuccess(t *testing.T) {
	db, mock := testutil.MockDB(t)
	testutil.ExpectClaimSuccess(mock)
	result, err := NewService(db).Claim(context.Background(), 7, 3, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if result.Claim.ID != 11 || result.Video.ID != 21 || result.Video.Status != "claimed" || result.UsedToday != 1 || result.DailyLimit != 100 || result.CategoryName != "test-category" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.HasPrefix(result.Claim.ClaimID, "clm_") || result.Claim.ClaimExpiresAt.Sub(result.Claim.ClaimedAt) != 10*time.Minute {
		t.Fatal("invalid claim identity or reservation")
	}
}

func TestClaimReplay(t *testing.T) {
	for _, name := range []string{"same", "different_category", "revoked_permission", "quota_query_failure"} {
		t.Run(name, func(t *testing.T) {
			db, mock := testutil.MockDB(t)
			testutil.ExpectUserLock(mock, 0, 0)
			mock.ExpectQuery("SELECT \\* FROM claims WHERE user_id").WithArgs(7, "same-key").WillReturnRows(testutil.ClaimRows(time.Now(), "reserved"))
			category := int64(3)
			if name == "different_category" {
				category = 4
			}
			mock.ExpectQuery("SELECT .* FROM videos WHERE id=\\$1").WithArgs(21).WillReturnRows(testutil.VideoRows(category))
			var want error
			switch name {
			case "different_category":
				want = ErrIdempotencyConflict
				mock.ExpectRollback()
			case "revoked_permission":
				mock.ExpectQuery("SELECT c.name.*FOR SHARE OF c, p").WithArgs(3, 7).WillReturnRows(sqlmock.NewRows([]string{"name"}))
				want = ErrCategoryForbidden
				mock.ExpectRollback()
			default:
				testutil.ExpectPermission(mock)
				q := mock.ExpectQuery("SELECT used_count FROM daily_quotas").WithArgs(7, sqlmock.AnyArg())
				if name == "quota_query_failure" {
					want = errors.New("quota read failure")
					q.WillReturnError(want)
					mock.ExpectRollback()
				} else {
					q.WillReturnRows(sqlmock.NewRows([]string{"used_count"}).AddRow(100))
					mock.ExpectCommit()
				}
			}
			result, err := NewService(db).Claim(context.Background(), 7, 3, "same-key")
			if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			if want == nil && (result.Claim.ClaimID != "clm_existing" || result.UsedToday != 100) {
				t.Fatalf("invalid replay: %+v", result)
			}
		})
	}
}

func TestClaimDenialsAndRollback(t *testing.T) {
	for _, name := range []string{"permission", "daily_zero", "daily_negative", "pending_zero", "pending_full", "quota_full", "no_video", "video_write", "claim_write", "commit"} {
		t.Run(name, func(t *testing.T) {
			db, mock := testutil.MockDB(t)
			daily, pending := 100, 3
			if name == "daily_zero" {
				daily = 0
			}
			if name == "daily_negative" {
				daily = -1
			}
			if name == "pending_zero" {
				pending = 0
			}
			testutil.ExpectUserLock(mock, daily, pending)
			testutil.ExpectNoClaim(mock)
			want := errors.New("injected write failure")
			if name == "permission" {
				mock.ExpectQuery("SELECT c.name.*FOR SHARE OF c, p").WithArgs(3, 7).WillReturnRows(sqlmock.NewRows([]string{"name"}))
				want = ErrCategoryForbidden
			} else {
				testutil.ExpectPermission(mock)
				switch name {
				case "daily_zero", "daily_negative":
					want = ErrQuotaExceeded
				case "pending_zero":
					want = ErrPendingLimit
				default:
					count := 0
					if name == "pending_full" {
						count = 3
					}
					testutil.ExpectPending(mock, count)
					if name == "pending_full" {
						want = ErrPendingLimit
					} else {
						if name == "quota_full" {
							mock.ExpectQuery("INSERT INTO daily_quotas.*RETURNING used_count").WillReturnRows(sqlmock.NewRows([]string{"used_count"}))
							want = ErrQuotaExceeded
						} else {
							testutil.ExpectQuota(mock)
							q := mock.ExpectQuery("SELECT .* FROM videos WHERE category_id=\\$1.*FOR UPDATE SKIP LOCKED").WithArgs(3)
							if name == "no_video" {
								q.WillReturnRows(sqlmock.NewRows([]string{"id"}))
								want = ErrNoVideo
							} else {
								q.WillReturnRows(testutil.VideoRows(3))
								update := mock.ExpectExec("UPDATE videos SET status='claimed'").WithArgs(sqlmock.AnyArg(), 21)
								if name == "video_write" {
									update.WillReturnError(want)
								} else {
									update.WillReturnResult(sqlmock.NewResult(0, 1))
									insert := mock.ExpectQuery("INSERT INTO claims.*RETURNING id")
									if name == "claim_write" {
										insert.WillReturnError(want)
									} else {
										insert.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
										mock.ExpectCommit().WillReturnError(want)
									}
								}
							}
						}
					}
				}
			}
			if name != "commit" {
				mock.ExpectRollback()
			}
			result, err := NewService(db).Claim(context.Background(), 7, 3, "same-key")
			if result != nil || !errors.Is(err, want) {
				t.Fatalf("result=%+v error=%v want=%v", result, err, want)
			}
		})
	}
}

func TestClaimInvalidInput(t *testing.T) {
	for _, input := range []struct {
		user, category int64
		key            string
	}{{0, 3, "key"}, {7, 0, "key"}, {7, -1, "key"}, {7, 3, "  "}, {7, 3, strings.Repeat("x", 129)}} {
		db, _ := testutil.MockDB(t)
		if _, err := NewService(db).Claim(context.Background(), input.user, input.category, input.key); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
	}
}

func TestClaimInactiveUser(t *testing.T) {
	db, mock := testutil.MockDB(t)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT u.id.*FOR UPDATE OF u").WithArgs(7).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "daily_limit", "pending_limit"}).AddRow(7, "disabled", 100, 3))
	mock.ExpectRollback()
	if _, err := NewService(db).Claim(context.Background(), 7, 3, "same-key"); !errors.Is(err, ErrUserDisabled) {
		t.Fatal(err)
	}
}
