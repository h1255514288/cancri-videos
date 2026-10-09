//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/control/api"
	"github.com/yourorg/video-distribution-go/internal/control/claim"
	"github.com/yourorg/video-distribution-go/internal/shared/auth"
	"github.com/yourorg/video-distribution-go/internal/shared/server"
	"github.com/yourorg/video-distribution-go/internal/shared/testutil"
)

func exec(t *testing.T, db *sqlx.DB, query string, args ...interface{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, db *sqlx.DB, query string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	if err := db.GetContext(ctx, &n, query); err != nil {
		t.Fatal(err)
	}
	return n
}

func fixture(t *testing.T, videos, daily, pending int) *sqlx.DB {
	t.Helper()
	db := testutil.Postgres(t)
	exec(t, db, `INSERT INTO user_groups (id, name, default_daily_quota, default_max_pending_claims) VALUES (7, 'test-group', $1, $2)`, daily, pending)
	exec(t, db, `INSERT INTO users (id, group_id) VALUES (7, 7), (8, 7)`)
	exec(t, db, `INSERT INTO categories (id, name) VALUES (3, 'test-category'), (4, 'other-category'), (5, 'forbidden-category')`)
	exec(t, db, `INSERT INTO user_category_permissions (user_id, category_id) VALUES (7, 3), (7, 4), (8, 3), (8, 4)`)
	exec(t, db, `INSERT INTO storage_nodes (id, node_name, storage_root_path, download_base_url) VALUES (2, 'test-node', '/test', 'http://localhost:8001')`)
	exec(t, db, `INSERT INTO videos (video_id, category_id, node_id, filename, storage_path, size_bytes, sha256, status)
	 SELECT 'vid_test_'||n, 3, 2, 'test.mp4', 'test/'||n, 100, repeat('a', 64), 'available' FROM generate_series(1, $1::integer) n`, videos)
	exec(t, db, `INSERT INTO api_keys (user_id, key_hash, key_prefix, expires_at) VALUES (7, $1, 'test-key', NOW()+INTERVAL '2 minutes')`, auth.HashAPIKey("test-api-key"))
	exec(t, db, `INSERT INTO api_keys (user_id, key_hash, key_prefix) VALUES (8, $1, 'other-key')`, auth.HashAPIKey("other-api-key"))
	return db
}

func TestConcurrentClaims(t *testing.T) {
	for _, scenario := range []string{"same_key", "quota", "pending", "distinct_users"} {
		t.Run(scenario, func(t *testing.T) {
			videos, daily, pending := 20, 100, 100
			if scenario == "quota" {
				daily = 3
			}
			if scenario == "pending" {
				pending = 3
			}
			if scenario == "distinct_users" {
				videos = 1
			}
			db := fixture(t, videos, daily, pending)
			svc := claim.NewService(db)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			const count = 12
			type outcome struct {
				result *claim.Result
				err    error
			}
			results := make(chan outcome, count)
			start := make(chan struct{})
			var workers sync.WaitGroup
			for i := 0; i < count; i++ {
				workers.Add(1)
				go func(i int) {
					defer workers.Done()
					<-start
					key, userID := fmt.Sprintf("key-%d", i), int64(7)
					if scenario == "same_key" {
						key = "same-key"
					}
					if scenario == "distinct_users" {
						userID += int64(i % 2)
					}
					r, err := svc.Claim(ctx, userID, 3, key)
					results <- outcome{r, err}
				}(i)
			}
			close(start)
			workers.Wait()
			close(results)
			successes := 0
			identities := map[string]bool{}
			videoIDs := map[int64]bool{}
			for r := range results {
				if r.err != nil {
					want := claim.ErrNoVideo
					if scenario == "quota" {
						want = claim.ErrQuotaExceeded
					}
					if scenario == "pending" {
						want = claim.ErrPendingLimit
					}
					if scenario == "same_key" || !errors.Is(r.err, want) {
						t.Errorf("unexpected concurrent result: %v", r.err)
					}
					continue
				}
				successes++
				identities[r.result.Claim.ClaimID] = true
				videoIDs[r.result.Video.ID] = true
			}
			wantClaims, wantSuccess := 1, 1
			if scenario == "same_key" {
				wantSuccess = count
			}
			if scenario == "quota" || scenario == "pending" {
				wantClaims, wantSuccess = 3, 3
			}
			if successes != wantSuccess || len(identities) != wantClaims || len(videoIDs) != wantClaims {
				t.Fatalf("success=%d claims=%d videos=%d", successes, len(identities), len(videoIDs))
			}
			if n := scalar(t, db, `SELECT COUNT(*) FROM claims`); n != wantClaims {
				t.Fatalf("claim count %d", n)
			}
			if n := scalar(t, db, `SELECT COALESCE(SUM(used_count),0) FROM daily_quotas`); n != wantClaims {
				t.Fatalf("quota count %d", n)
			}
			if n := scalar(t, db, `SELECT COUNT(*) FROM videos WHERE status='claimed'`); n != wantClaims {
				t.Fatalf("claimed video count %d", n)
			}
		})
	}
}

func TestLimitsPermissionsAndRollback(t *testing.T) {
	for _, scenario := range []string{"zero", "negative", "pending_zero", "forbidden", "disabled_category", "disabled_user", "empty", "insert_failure", "group_limit", "user_override", "system_defaults", "expired_pending"} {
		t.Run(scenario, func(t *testing.T) {
			videos := 4
			if scenario == "empty" {
				videos = 0
			}
			db := fixture(t, videos, 2, 2)
			svc := claim.NewService(db)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			category := int64(3)
			var want error
			switch scenario {
			case "zero":
				exec(t, db, `UPDATE users SET daily_quota=0 WHERE id=7`)
				want = claim.ErrQuotaExceeded
			case "negative":
				exec(t, db, `UPDATE users SET daily_quota=-1 WHERE id=7`)
				want = claim.ErrQuotaExceeded
			case "pending_zero":
				exec(t, db, `UPDATE users SET max_pending_claims=0 WHERE id=7`)
				want = claim.ErrPendingLimit
			case "forbidden":
				category = 5
				want = claim.ErrCategoryForbidden
			case "disabled_category":
				exec(t, db, `UPDATE categories SET status='disabled' WHERE id=3`)
				want = claim.ErrCategoryForbidden
			case "disabled_user":
				exec(t, db, `UPDATE users SET status='disabled' WHERE id=7`)
				want = claim.ErrUserDisabled
			case "empty":
				want = claim.ErrNoVideo
			case "insert_failure":
				exec(t, db, `ALTER TABLE claims ADD CONSTRAINT test_reject_claim CHECK (user_id<>7)`)
			case "user_override":
				exec(t, db, `UPDATE users SET daily_quota=1 WHERE id=7`)
			case "system_defaults":
				exec(t, db, `UPDATE users SET group_id=NULL WHERE id=7`)
			case "expired_pending":
				exec(t, db, `UPDATE users SET max_pending_claims=1 WHERE id=7`)
				if _, err := svc.Claim(ctx, 7, 3, "expired-key"); err != nil {
					t.Fatal(err)
				}
				exec(t, db, `UPDATE claims SET claim_expires_at=NOW()-INTERVAL '1 second' WHERE user_id=7`)
			}
			result, err := svc.Claim(ctx, 7, category, "test-key")
			if scenario == "insert_failure" {
				if err == nil {
					t.Fatal("injected insert failure was ignored")
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			if err != nil {
				if scalar(t, db, `SELECT COUNT(*) FROM claims`) != 0 || scalar(t, db, `SELECT COUNT(*) FROM daily_quotas`) != 0 || scalar(t, db, `SELECT COUNT(*) FROM videos WHERE status='claimed'`) != 0 {
					t.Fatal("failed transaction changed state")
				}
				return
			}
			limit := 2
			if scenario == "user_override" {
				limit = 1
			}
			if scenario == "system_defaults" {
				limit = 100
			}
			if result.DailyLimit != limit || result.Video.Title != "" || result.Video.MimeType != "" {
				t.Fatalf("bad inherited limits / nullable fields: %+v", result)
			}
			if scenario == "user_override" {
				if _, err := svc.Claim(ctx, 7, 3, "second-key"); !errors.Is(err, claim.ErrQuotaExceeded) {
					t.Fatalf("override not enforced: %v", err)
				}
			}
		})
	}
}

func TestControlHTTPChain(t *testing.T) {
	db := fixture(t, 3, 100, 3)
	const secret = "integration-test-secret"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(server.ApplicationHandler("control", db, secret, logger))
	defer srv.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	send := func(method, path, key, body string, want int, target interface{}) {
		t.Helper()
		r, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		r.Header.Set("Idempotency-Key", "same-key")
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			data, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		if target != nil {
			if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
				t.Fatal(err)
			}
		}
	}
	send("GET", "/health/ready", "", "", 200, nil)
	var first, replay api.ClaimResponse
	send("POST", "/api/v1/claims", "test-api-key", `{"category_id":3}`, 201, &first)
	send("POST", "/api/v1/claims", "test-api-key", `{"category_id":3}`, 201, &replay)
	if first.ClaimID != replay.ClaimID || first.Video.ID != replay.Video.ID || replay.Quota.UsedToday != 1 || first.Download == nil {
		t.Fatal("idempotency/response failure")
	}
	token := strings.TrimPrefix(first.Download.URL, "http://localhost:8001/d/")
	claims, err := auth.VerifyDownloadToken(token, secret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ClaimID != first.ClaimID || !claims.ExpiresAt.Time.Equal(first.Download.ExpiresAt) {
		t.Fatal("JWT does not match response")
	}
	var keyExpiry time.Time
	if err := db.Get(&keyExpiry, `SELECT expires_at FROM api_keys WHERE user_id=7`); err != nil {
		t.Fatal(err)
	}
	if claims.ExpiresAt.Time.After(keyExpiry) || claims.ExpiresAt.Time.After(first.Expires.ClaimExpiresAt) {
		t.Fatal("credential outlives authorization")
	}
	var detail api.ClaimDetail
	send("GET", "/api/v1/claims/"+first.ClaimID, "test-api-key", "", 200, &detail)
	if detail.ClaimID != first.ClaimID || detail.Status != "reserved" {
		t.Fatal("query mismatch")
	}
	send("GET", "/api/v1/claims/"+first.ClaimID, "other-api-key", "", 404, nil)
	send("POST", "/api/v1/claims", "test-api-key", `{"category_id":4}`, 409, nil)
	if scalar(t, db, `SELECT COUNT(*) FROM claims`) != 1 || scalar(t, db, `SELECT used_count FROM daily_quotas WHERE user_id=7`) != 1 {
		t.Fatal("replay changed quota")
	}
	if scalar(t, db, `SELECT COUNT(*) FROM api_keys WHERE user_id=7 AND last_used_at IS NOT NULL`) != 1 {
		t.Fatal("last use not recorded")
	}
	exec(t, db, `UPDATE claims SET status='completed' WHERE user_id=7`)
	send("POST", "/api/v1/claims", "test-api-key", `{"category_id":3}`, 201, &replay)
	if replay.ClaimID != first.ClaimID || replay.Download != nil {
		t.Fatal("completed claim issued a token")
	}
}
