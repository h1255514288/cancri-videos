package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/control/claim"
	"github.com/yourorg/video-distribution-go/internal/shared/auth"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
)

type Handler struct {
	db            *sqlx.DB
	claimService  *claim.Service
	jwtSecret     string
	logger        *slog.Logger
	authenticated http.Handler
}

func NewHandler(db *sqlx.DB, jwtSecret string, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{db: db, claimService: claim.NewService(db), jwtSecret: jwtSecret, logger: logger}
	h.authenticated = auth.APIKeyAuth(db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			h.createClaim(w, r)
		} else {
			h.getClaim(w, r)
		}
	}))
	return h
}

type ClaimRequest struct {
	CategoryID int64 `json:"category_id"`
}

type ClaimResponse struct {
	ClaimID  string        `json:"claim_id"`
	Status   string        `json:"status"`
	Video    VideoInfo     `json:"video"`
	Download *DownloadInfo `json:"download"`
	Quota    QuotaInfo     `json:"quota"`
	Expires  ExpiryInfo    `json:"expires"`
}

type VideoInfo struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Filename     string `json:"filename"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	CategoryID   int64  `json:"category_id"`
	CategoryName string `json:"category_name"`
}

type DownloadInfo struct {
	URL            string    `json:"url"`
	ExpiresAt      time.Time `json:"expires_at"`
	SupportsResume bool      `json:"supports_resume"`
}

type QuotaInfo struct {
	DailyLimit int `json:"daily_limit"`
	UsedToday  int `json:"used_today"`
	Remaining  int `json:"remaining"`
}

type ExpiryInfo struct {
	ClaimExpiresAt time.Time  `json:"claim_expires_at"`
	RetryDeadline  *time.Time `json:"retry_deadline"`
}

type ClaimDetail struct {
	ClaimID         string     `json:"claim_id"`
	Status          string     `json:"status"`
	ClaimedAt       time.Time  `json:"claimed_at"`
	ClaimExpiresAt  time.Time  `json:"claim_expires_at"`
	FirstDownloadAt *time.Time `json:"first_download_at"`
	RetryDeadline   *time.Time `json:"retry_deadline"`
	CompletedAt     *time.Time `json:"completed_at"`
	RetryCount      int        `json:"retry_count"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	method := ""
	if r.URL.Path == "/api/v1/claims" {
		method = http.MethodPost
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/claims/") {
		method = http.MethodGet
	}
	if method == "" {
		h.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	h.authenticated.ServeHTTP(w, r)
}

func (h *Handler) createClaim(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	key, valid := idempotencyKey(r)
	if !valid {
		h.writeError(w, http.StatusBadRequest, "invalid_idempotency_key")
		return
	}

	var req ClaimRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		var extra interface{}
		if endErr := decoder.Decode(&extra); !errors.Is(endErr, io.EOF) {
			if endErr == nil {
				err = claim.ErrInvalidRequest
			} else {
				err = endErr
			}
		}
	}
	if err != nil || req.CategoryID <= 0 {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			h.writeError(w, http.StatusBadRequest, "invalid_request")
		}
		return
	}

	result, err := h.claimService.Claim(r.Context(), user.ID, req.CategoryID, key)
	if err != nil {
		h.writeClaimError(w, err)
		return
	}
	record, video := result.Claim, result.Video
	remaining := result.DailyLimit - result.UsedToday
	if remaining < 0 {
		remaining = 0
	}
	response := ClaimResponse{
		ClaimID: record.ClaimID, Status: record.Status,
		Video: VideoInfo{ID: video.VideoID, Title: video.Title, Filename: video.Filename, SizeBytes: video.SizeBytes,
			SHA256: video.SHA256, CategoryID: video.CategoryID, CategoryName: result.CategoryName},
		Quota:   QuotaInfo{DailyLimit: result.DailyLimit, UsedToday: result.UsedToday, Remaining: remaining},
		Expires: ExpiryInfo{ClaimExpiresAt: record.ClaimExpiresAt, RetryDeadline: record.RetryDeadline},
	}
	if expiry, available := linkExpiry(record, user, time.Now().UTC()); available {
		var nodeURL string
		if err := h.db.GetContext(r.Context(), &nodeURL, `SELECT download_base_url FROM storage_nodes WHERE id=$1`, video.NodeID); err != nil {
			h.logger.Error("get download node failed", "error", err)
			h.writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		token, err := auth.SignDownloadTokenUntil(record.ClaimID, video.VideoID, video.NodeID, record.CredentialVersion, expiry, h.jwtSecret)
		if err != nil {
			h.logger.Error("sign download token failed", "error", err)
			h.writeError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		response.Download = &DownloadInfo{URL: strings.TrimRight(nodeURL, "/") + "/d/" + token, ExpiresAt: expiry, SupportsResume: false}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(response)
}

func idempotencyKey(r *http.Request) (string, bool) {
	if len(r.Header.Values("Idempotency-Key")) > 1 || len(r.Header.Values("X-Idempotency-Key")) > 1 {
		return "", false
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	alias := strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
	if key != "" && alias != "" && key != alias {
		return "", false
	}
	if key == "" {
		key = alias
	}
	return key, key != "" && len(key) <= 128
}

func linkExpiry(record *model.ClaimRecord, user *auth.User, now time.Time) (time.Time, bool) {
	state := model.Claim{State: model.ClaimState(record.Status), ReservedUntil: record.ClaimExpiresAt, Attempts: record.RetryCount}
	if record.FirstDownloadAt != nil {
		state.FirstStartedAt = *record.FirstDownloadAt
	}
	if record.RetryDeadline != nil {
		state.RetryUntil = *record.RetryDeadline
	}
	key := model.KeyState{Enabled: true}
	if user.KeyExpiresAt != nil {
		key.ExpiresAt = *user.KeyExpiresAt
	}
	expiry, err := state.LinkExpiry(now, key)
	expiry = expiry.Truncate(time.Second)
	return expiry, err == nil && record.RetryCount < model.MaxAttempts && now.Before(expiry)
}

func (h *Handler) getClaim(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/claims/")
	if strings.TrimSpace(id) == "" || len(id) > 32 || strings.Contains(id, "/") {
		h.writeError(w, http.StatusBadRequest, "invalid_claim_id")
		return
	}
	record, err := h.claimService.GetClaim(r.Context(), id, user.ID)
	if errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		h.logger.Error("get claim failed", "error", err)
		h.writeError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ClaimDetail{ClaimID: record.ClaimID, Status: record.Status, ClaimedAt: record.ClaimedAt,
		ClaimExpiresAt: record.ClaimExpiresAt, FirstDownloadAt: record.FirstDownloadAt, RetryDeadline: record.RetryDeadline,
		CompletedAt: record.CompletedAt, RetryCount: record.RetryCount})
}

func (h *Handler) writeClaimError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, claim.ErrInvalidRequest):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, claim.ErrIdempotencyConflict):
		status, code = http.StatusConflict, "idempotency_conflict"
	case errors.Is(err, claim.ErrNoVideo):
		status, code = http.StatusNotFound, "no_available_video"
	case errors.Is(err, claim.ErrQuotaExceeded):
		status, code = http.StatusForbidden, "quota_exceeded"
	case errors.Is(err, claim.ErrPendingLimit):
		status, code = http.StatusForbidden, "pending_claim_limit"
	case errors.Is(err, claim.ErrCategoryForbidden):
		status, code = http.StatusForbidden, "category_forbidden"
	case errors.Is(err, claim.ErrUserDisabled):
		status, code = http.StatusForbidden, "user_disabled"
	default:
		h.logger.Error("claim failed", "error", err)
	}
	h.writeError(w, status, code)
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "detail": ""})
}
