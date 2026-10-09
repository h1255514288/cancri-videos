package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestAPIKeyAuth(t *testing.T) {
	for _, name := range []string{"missing", "empty", "scheme", "multiple", "unknown", "database", "revoked", "future", "expired", "disabled", "blacklisted", "missing_user", "user_database", "valid", "nullable", "usage_failure"} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.MatchExpectationsInOrder(true)
			header, want := "bearer test-api-key", http.StatusUnauthorized
			now := time.Now().UTC()
			if name == "missing" {
				header = ""
			}
			if name == "empty" {
				header = "Bearer "
			}
			if name == "scheme" {
				header = "Basic test-api-key"
			}
			if name != "missing" && name != "empty" && name != "scheme" && name != "multiple" {
				q := mock.ExpectQuery("SELECT \\* FROM api_keys WHERE key_hash=\\$1").WithArgs(HashAPIKey("test-api-key"))
				keyRows := sqlmock.NewRows([]string{"id", "user_id", "status", "effective_at", "expires_at"})
				switch name {
				case "unknown":
					q.WillReturnRows(keyRows)
				case "database":
					q.WillReturnError(errors.New("private database failure"))
					want = 500
				case "revoked":
					q.WillReturnRows(keyRows.AddRow(1, 7, "revoked", nil, nil))
				case "future":
					q.WillReturnRows(keyRows.AddRow(1, 7, "active", now.Add(time.Hour), nil))
				case "expired":
					q.WillReturnRows(keyRows.AddRow(1, 7, "active", nil, now.Add(-time.Second)))
				default:
					if name == "nullable" {
						q.WillReturnRows(keyRows.AddRow(1, 7, "active", nil, nil))
					} else {
						q.WillReturnRows(keyRows.AddRow(1, 7, "active", now.Add(-time.Hour), now.Add(time.Hour)))
					}
					users := sqlmock.NewRows([]string{"id", "status", "daily_quota", "internal_name"})
					uq := mock.ExpectQuery("SELECT \\* FROM users WHERE id=\\$1").WithArgs(7)
					switch name {
					case "missing_user":
						uq.WillReturnRows(users)
					case "user_database":
						uq.WillReturnError(errors.New("private user failure"))
						want = 500
					case "disabled", "blacklisted":
						uq.WillReturnRows(users.AddRow(7, name, nil, nil))
						want = 403
					default:
						uq.WillReturnRows(users.AddRow(7, "active", nil, nil))
						want = 204
						update := mock.ExpectExec("UPDATE api_keys SET last_used_at=NOW\\(\\) WHERE id=\\$1").WithArgs(1)
						if name == "usage_failure" {
							update.WillReturnError(errors.New("private usage failure"))
						} else {
							update.WillReturnResult(sqlmock.NewResult(0, 1))
						}
					}
				}
			}
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				u, ok := GetUser(r.Context())
				if !ok || u.ID != 7 || u.Status != "active" {
					t.Fatalf("invalid authenticated user: %+v", u)
				}
				if (u.KeyExpiresAt == nil) != (name == "nullable") {
					t.Fatal("key expiry lost")
				}
				w.WriteHeader(http.StatusNoContent)
			})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/claims", nil)
			if header != "" {
				r.Header.Set("Authorization", header)
			}
			if name == "multiple" {
				r.Header.Add("Authorization", header)
			}
			w := httptest.NewRecorder()
			APIKeyAuth(sqlx.NewDb(db, "sqlmock"))(next).ServeHTTP(w, r)
			if w.Code != want || called != (want == 204) {
				t.Fatalf("status=%d body=%s called=%v", w.Code, w.Body.String(), called)
			}
			if want != 204 && w.Header().Get("Content-Type") != "application/json" {
				t.Fatal("non-JSON error")
			}
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal("database error leaked")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGetUserMissing(t *testing.T) {
	if _, ok := GetUser(context.Background()); ok {
		t.Fatal("missing user accepted")
	}
	if _, ok := GetUser(context.WithValue(context.Background(), UserContextKey, (*User)(nil))); ok {
		t.Fatal("nil user accepted")
	}
}
