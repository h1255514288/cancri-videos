package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yourorg/video-distribution-go/internal/control/claim"
	"github.com/yourorg/video-distribution-go/internal/shared/auth"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
	"github.com/yourorg/video-distribution-go/internal/shared/testutil"
)

func expectAuth(mock sqlmock.Sqlmock, expiry *time.Time) {
	mock.ExpectQuery("SELECT \\* FROM api_keys WHERE key_hash=\\$1").WithArgs(auth.HashAPIKey("test-api-key")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "status", "effective_at", "expires_at"}).AddRow(1, 7, "active", nil, expiry))
	mock.ExpectQuery("SELECT \\* FROM users WHERE id=\\$1").WithArgs(7).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow(7, "active"))
	mock.ExpectExec("UPDATE api_keys SET last_used_at=NOW\\(\\) WHERE id=\\$1").WithArgs(1).WillReturnResult(sqlmock.NewResult(0, 1))
}

func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-api-key")
	r.Header.Set("Idempotency-Key", "same-key")
	return r
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestClaimInputValidation(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       int
	}{
		{"no_key", `{"category_id":3}`, 400}, {"key_conflict", `{"category_id":3}`, 400},
		{"key_long", `{"category_id":3}`, 400}, {"duplicate_key", `{"category_id":3}`, 400},
		{"zero", `{"category_id":0}`, 400}, {"negative", `{"category_id":-1}`, 400},
		{"missing", `{}`, 400}, {"null", `null`, 400}, {"fraction", `{"category_id":1.5}`, 400},
		{"invalid", `{`, 400}, {"multiple", `{"category_id":3}{"category_id":4}`, 400},
		{"trailing", `{"category_id":3} invalid`, 400}, {"unknown_field", `{"category_id":3,"user_id":99}`, 400},
		{"oversized", `{"category_id":3}` + strings.Repeat(" ", 4096), 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock := testutil.MockDB(t)
			expectAuth(mock, nil)
			r := request("POST", "/api/v1/claims", tt.body)
			switch tt.name {
			case "no_key":
				r.Header.Del("Idempotency-Key")
			case "key_conflict":
				r.Header.Set("X-Idempotency-Key", "different")
			case "key_long":
				r.Header.Set("Idempotency-Key", strings.Repeat("x", 129))
			case "duplicate_key":
				r.Header.Add("Idempotency-Key", "same-key")
			}
			w := httptest.NewRecorder()
			NewHandler(db, "test-secret", testLogger()).ServeHTTP(w, r)
			if w.Code != tt.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestClaimSuccessHTTP(t *testing.T) {
	for _, header := range []string{"Idempotency-Key", "X-Idempotency-Key"} {
		t.Run(header, func(t *testing.T) {
			db, mock := testutil.MockDB(t)
			expiry := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
			expectAuth(mock, &expiry)
			testutil.ExpectClaimSuccess(mock)
			mock.ExpectQuery("SELECT download_base_url FROM storage_nodes WHERE id=\\$1").WithArgs(2).
				WillReturnRows(sqlmock.NewRows([]string{"download_base_url"}).AddRow("http://localhost:8001/"))
			r := request("POST", "/api/v1/claims", `{"category_id":3}`)
			r.Header.Del("Idempotency-Key")
			r.Header.Set(header, "same-key")
			server := httptest.NewServer(NewHandler(db, "test-secret", testLogger()))
			defer server.Close()
			r.URL.Scheme = "http"
			r.URL.Host = strings.TrimPrefix(server.URL, "http://")
			r.RequestURI = ""
			client := &http.Client{Timeout: 3 * time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 201 {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("%d: %s", resp.StatusCode, b)
			}
			var result ClaimResponse
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result.Download == nil || result.Video.CategoryName != "test-category" || result.Status != "reserved" || result.Quota.Remaining != 99 {
				t.Fatalf("invalid response: %+v", result)
			}
			if !strings.HasPrefix(result.Download.URL, "http://localhost:8001/d/") {
				t.Fatal(result.Download.URL)
			}
			token := strings.TrimPrefix(result.Download.URL, "http://localhost:8001/d/")
			claims, err := auth.VerifyDownloadToken(token, "test-secret")
			if err != nil {
				t.Fatal(err)
			}
			if claims.ClaimID != result.ClaimID || claims.VideoID != result.Video.ID || !claims.ExpiresAt.Time.Equal(expiry) || !result.Download.ExpiresAt.Equal(expiry) {
				t.Fatal("credential/response mismatch")
			}
			if resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("response cacheable")
			}
		})
	}
}

func TestClaimAuthenticationAndRoutes(t *testing.T) {
	for _, name := range []string{"missing_auth", "invalid_key", "method", "unknown"} {
		t.Run(name, func(t *testing.T) {
			db, mock := testutil.MockDB(t)
			r := request("POST", "/api/v1/claims", `{"category_id":3}`)
			want := 401
			switch name {
			case "missing_auth":
				r.Header.Del("Authorization")
			case "invalid_key":
				mock.ExpectQuery("SELECT \\* FROM api_keys WHERE key_hash=\\$1").WithArgs(auth.HashAPIKey("test-api-key")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			case "method":
				r.Method = "PUT"
				want = 405
			case "unknown":
				r.URL.Path = "/missing"
				want = 404
			}
			w := httptest.NewRecorder()
			NewHandler(db, "test-secret", testLogger()).ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestClaimServiceFailureHTTP(t *testing.T) {
	db, mock := testutil.MockDB(t)
	expectAuth(mock, nil)
	mock.ExpectBegin().WillReturnError(errors.New("private failure"))
	w := httptest.NewRecorder()
	NewHandler(db, "test-secret", testLogger()).ServeHTTP(w, request("POST", "/api/v1/claims", `{"category_id":3}`))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestClaimErrorMapping(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{claim.ErrInvalidRequest, 400, "invalid_request"}, {claim.ErrIdempotencyConflict, 409, "idempotency_conflict"},
		{claim.ErrNoVideo, 404, "no_available_video"}, {claim.ErrQuotaExceeded, 403, "quota_exceeded"},
		{claim.ErrPendingLimit, 403, "pending_claim_limit"}, {claim.ErrCategoryForbidden, 403, "category_forbidden"},
		{claim.ErrUserDisabled, 403, "user_disabled"}, {errors.New("private failure"), 500, "internal_error"},
	} {
		w := httptest.NewRecorder()
		h := &Handler{logger: testLogger()}
		h.writeClaimError(w, fmt.Errorf("wrapped: %w", tt.err))
		var response map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != tt.status || response["error"] != tt.code || response["detail"] != "" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestGetClaimHTTP(t *testing.T) {
	for _, name := range []string{"success", "other_user", "failure", "invalid_id"} {
		t.Run(name, func(t *testing.T) {
			db, mock := testutil.MockDB(t)
			expectAuth(mock, nil)
			path, want := "/api/v1/claims/clm_existing", 200
			if name == "invalid_id" {
				path = "/api/v1/claims/"
				want = 400
			} else {
				q := mock.ExpectQuery("SELECT \\* FROM claims WHERE claim_id=\\$1 AND user_id=\\$2").WithArgs("clm_existing", 7)
				switch name {
				case "success":
					q.WillReturnRows(testutil.ClaimRows(time.Now(), "reserved"))
				case "other_user":
					q.WillReturnRows(sqlmock.NewRows([]string{"id"}))
					want = 404
				case "failure":
					q.WillReturnError(errors.New("private failure"))
					want = 500
				}
			}
			w := httptest.NewRecorder()
			NewHandler(db, "test-secret", testLogger()).ServeHTTP(w, request("GET", path, ""))
			if w.Code != want || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if want == 200 {
				var detail map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
					t.Fatal(err)
				}
				if detail["claim_id"] != "clm_existing" || detail["status"] != "reserved" {
					t.Fatal(detail)
				}
				for _, key := range []string{"ID", "UserID", "VideoID", "id", "user_id", "idempotency_key", "credential_version"} {
					if _, ok := detail[key]; ok {
						t.Fatalf("internal field exposed: %s", key)
					}
				}
			}
		})
	}
}

func TestLinkExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, name := range []string{"ttl", "reservation", "key", "retry", "completed", "expired", "streaming", "exhausted"} {
		t.Run(name, func(t *testing.T) {
			record := &model.ClaimRecord{Status: "reserved", ClaimExpiresAt: now.Add(10 * time.Minute)}
			user := &auth.User{}
			want, valid := now.Add(model.LinkTTL), true
			switch name {
			case "reservation":
				record.ClaimExpiresAt = now.Add(time.Minute)
				want = record.ClaimExpiresAt
			case "key":
				expiry := now.Add(2 * time.Minute)
				user.KeyExpiresAt = &expiry
				want = expiry
			case "retry":
				first, end := now.Add(-time.Minute), now.Add(3*time.Minute)
				record.FirstDownloadAt, record.RetryDeadline, record.Status = &first, &end, "retryable"
				want = end
			case "completed", "streaming":
				record.Status = name
				valid = false
			case "expired":
				record.ClaimExpiresAt = now
				valid = false
			case "exhausted":
				record.RetryCount = model.MaxAttempts
				valid = false
			}
			expiry, available := linkExpiry(record, user, now)
			if available != valid || (valid && !expiry.Equal(want)) {
				t.Fatalf("got %v %v want %v %v", expiry, available, want, valid)
			}
		})
	}
}

func TestClosedClaimReplayHTTP(t *testing.T) {
	for _, state := range []string{"completed", "expired"} {
		t.Run(state, func(t *testing.T) {
			db, mock := testutil.MockDB(t)
			expectAuth(mock, nil)
			testutil.ExpectUserLock(mock, 0, 0)
			now, status := time.Now(), state
			if state == "expired" {
				now, status = now.Add(-time.Hour), "reserved"
			}
			mock.ExpectQuery("SELECT \\* FROM claims WHERE user_id").WithArgs(7, "same-key").WillReturnRows(testutil.ClaimRows(now, status))
			mock.ExpectQuery("SELECT .* FROM videos WHERE id=\\$1").WithArgs(21).WillReturnRows(testutil.VideoRows(3))
			testutil.ExpectPermission(mock)
			mock.ExpectQuery("SELECT used_count FROM daily_quotas").WillReturnRows(sqlmock.NewRows([]string{"used_count"}).AddRow(1))
			mock.ExpectCommit()
			w := httptest.NewRecorder()
			NewHandler(db, "test-secret", testLogger()).ServeHTTP(w, request("POST", "/api/v1/claims", `{"category_id":3}`))
			var response ClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != 201 || response.Download != nil || response.ClaimID != "clm_existing" || response.Quota.Remaining != 0 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}
