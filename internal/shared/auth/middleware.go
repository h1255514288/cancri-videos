package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
)

type contextKey string

const UserContextKey contextKey = "user"

type User struct {
	model.User
	KeyExpiresAt *time.Time
}

func APIKeyAuth(db *sqlx.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			values := r.Header.Values("Authorization")
			if len(values) != 1 {
				writeAuthError(w, http.StatusUnauthorized, "invalid_auth")
				return
			}
			parts := strings.Fields(values[0])
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 4096 {
				writeAuthError(w, http.StatusUnauthorized, "invalid_auth")
				return
			}

			var key model.APIKey
			err := db.GetContext(r.Context(), &key, `SELECT * FROM api_keys WHERE key_hash=$1`, HashAPIKey(parts[1]))
			if errors.Is(err, sql.ErrNoRows) {
				writeAuthError(w, http.StatusUnauthorized, "invalid_key")
				return
			}
			if err != nil {
				writeAuthError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if key.Status != "active" {
				writeAuthError(w, http.StatusUnauthorized, "key_revoked")
				return
			}
			now := time.Now().UTC()
			if key.EffectiveAt != nil && now.Before(*key.EffectiveAt) {
				writeAuthError(w, http.StatusUnauthorized, "key_not_effective")
				return
			}
			if key.ExpiresAt != nil && !now.Before(*key.ExpiresAt) {
				writeAuthError(w, http.StatusUnauthorized, "key_expired")
				return
			}

			var user model.User
			err = db.GetContext(r.Context(), &user, `SELECT * FROM users WHERE id=$1`, key.UserID)
			if errors.Is(err, sql.ErrNoRows) {
				writeAuthError(w, http.StatusUnauthorized, "invalid_key")
				return
			}
			if err != nil {
				writeAuthError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if user.Status != "active" {
				writeAuthError(w, http.StatusForbidden, "user_disabled")
				return
			}

			updateCtx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
			_, err = db.ExecContext(updateCtx, `UPDATE api_keys SET last_used_at=NOW() WHERE id=$1`, key.ID)
			cancel()
			if err != nil {
				slog.Warn("could not update API key last use")
			}
			ctx := context.WithValue(r.Context(), UserContextKey, &User{User: user, KeyExpiresAt: key.ExpiresAt})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetUser(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(UserContextKey).(*User)
	return user, ok && user != nil
}

func writeAuthError(w http.ResponseWriter, status int, code string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "detail": ""})
}
