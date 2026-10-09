package admin

import (
	"context"
	"database/sql"
	"errors"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	service *Service
	logger  *slog.Logger
}

func NewHandler(service *Service, logger *slog.Logger) *Handler {
	if logger == nil { logger = slog.Default() }
	return &Handler{
		service: service,
		logger:  logger,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Remove /admin prefix
	path := strings.TrimPrefix(r.URL.Path, "/admin")
	if path != "/login" {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts)!=2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w,"unauthorized",http.StatusUnauthorized); return
		}
		id,err:=h.service.authenticate(r.Context(),parts[1])
		if err!=nil {
			if errors.Is(err,ErrInvalidCredentials) || errors.Is(err,sql.ErrNoRows) {
				http.Error(w,"unauthorized",http.StatusUnauthorized)
			} else { http.Error(w,"internal_error",http.StatusInternalServerError) }
			return
		}
		r=r.WithContext(context.WithValue(r.Context(),adminContextKey{},id))
	}
	switch {
	case path == "/login" && r.Method == "POST":
		h.handleLogin(w, r)
	case path == "/videos" && r.Method == "POST":
		h.handleUpload(w, r)
	case path == "/users" && r.Method == "POST":
		h.handleCreateUser(w, r)
	case strings.HasPrefix(path, "/users/") && r.Method == "GET":
		h.handleGetUser(w, r, path)
	case strings.HasPrefix(path, "/users/") && r.Method == "PATCH":
		h.handleUpdateUser(w, r, path)
	case path == "/api_keys" && r.Method == "POST":
		h.handleCreateAPIKey(w, r)
	case strings.HasPrefix(path, "/api_keys/") && strings.HasSuffix(path, "/revoke") && r.Method == "POST":
		h.handleRevokeAPIKey(w, r, path)
	case path == "/categories" && r.Method == "POST":
		h.handleCreateCategory(w, r)
	case path == "/categories" && r.Method == "GET":
		h.handleListCategories(w, r)
	case strings.HasPrefix(path, "/users/") && strings.Contains(path, "/permissions/categories/") && r.Method == "POST":
		h.handleGrantPermission(w, r, path)
	default:
		http.Error(w, "not_found", http.StatusNotFound)
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if !decodeRequest(w, r, &req) { return }

	if req.Username == "" || req.Password == "" {
		http.Error(w, "missing_credentials", http.StatusBadRequest)
		return
	}

	resp, err := h.service.Login(r.Context(), req)
	if err != nil {
		if err == ErrInvalidCredentials {
			http.Error(w, "invalid_credentials", http.StatusUnauthorized)
			return
		}
		if writeServiceError(w, err) { return }
		h.logger.Error("login failed", "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req CreateUserRequest
	if !decodeRequest(w, r, &req) { return }

	resp, err := h.service.CreateUser(r.Context(), req)
	if err != nil {
		if writeServiceError(w, err) { return }
		h.logger.Error("create user failed", "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleGetUser(w http.ResponseWriter, r *http.Request, path string) {
	idStr := strings.TrimPrefix(path, "/users/")
	userID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || userID <= 0 {
		http.Error(w, "invalid_user_id", http.StatusBadRequest)
		return
	}

	resp, err := h.service.GetUser(r.Context(), userID)
	if err != nil {
		if err == ErrUserNotFound {
			http.Error(w, "user_not_found", http.StatusNotFound)
			return
		}
		h.logger.Error("get user failed", "user_id", userID, "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleUpdateUser(w http.ResponseWriter, r *http.Request, path string) {
	idStr := strings.TrimPrefix(path, "/users/")
	userID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || userID <= 0 {
		http.Error(w, "invalid_user_id", http.StatusBadRequest)
		return
	}

	var req UpdateUserRequest
	if !decodeRequest(w, r, &req) { return }

	resp, err := h.service.UpdateUser(r.Context(), userID, req)
	if err != nil {
		if err == ErrUserNotFound {
			http.Error(w, "user_not_found", http.StatusNotFound)
			return
		}
		if writeServiceError(w, err) { return }
		h.logger.Error("update user failed", "user_id", userID, "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req CreateAPIKeyRequest
	if !decodeRequest(w, r, &req) { return }

	if req.UserID <= 0 {
		http.Error(w, "missing_user_id", http.StatusBadRequest)
		return
	}

	resp, err := h.service.CreateAPIKey(r.Context(), req)
	if err != nil {
		if err == ErrUserNotFound {
			http.Error(w, "user_not_found", http.StatusNotFound)
			return
		}
		if writeServiceError(w, err) { return }
		h.logger.Error("create api key failed", "user_id", req.UserID, "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request, path string) {
	// path like /api_keys/123/revoke
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 || parts[0] != "api_keys" || parts[2] != "revoke" {
		http.Error(w, "invalid_path", http.StatusBadRequest)
		return
	}
	
	keyID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || keyID <= 0 {
		http.Error(w, "invalid_key_id", http.StatusBadRequest)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeRequest(w, r, &req) { return }

	adminID := r.Context().Value(adminContextKey{}).(int64)
	
	err = h.service.RevokeAPIKey(r.Context(), keyID, adminID, req.Reason)
	if err != nil {
		if writeServiceError(w, err) { return }
		h.logger.Error("revoke api key failed", "key_id", keyID, "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	var req CreateCategoryRequest
	if !decodeRequest(w, r, &req) { return }

	if req.Name == "" {
		http.Error(w, "missing_name", http.StatusBadRequest)
		return
	}

	resp, err := h.service.CreateCategory(r.Context(), req)
	if err != nil {
		if err == ErrCategoryExists {
			http.Error(w, "category_exists", http.StatusConflict)
			return
		}
		if writeServiceError(w, err) { return }
		h.logger.Error("create category failed", "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) handleListCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := h.service.ListCategories(r.Context())
	if err != nil {
		h.logger.Error("list categories failed", "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"categories": categories,
	})
}

func (h *Handler) handleGrantPermission(w http.ResponseWriter, r *http.Request, path string) {
	// path like /users/123/permissions/categories/456
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "users" || parts[2] != "permissions" || parts[3] != "categories" {
		http.Error(w, "invalid_path", http.StatusBadRequest)
		return
	}

	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || userID <= 0 {
		http.Error(w, "invalid_user_id", http.StatusBadRequest)
		return
	}

	categoryID, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || categoryID <= 0 {
		http.Error(w, "invalid_category_id", http.StatusBadRequest)
		return
	}

	adminID := r.Context().Value(adminContextKey{}).(int64)

	err = h.service.GrantCategoryPermission(r.Context(), userID, categoryID, adminID)
	if err != nil {
		h.logger.Error("grant permission failed", "user_id", userID, "category_id", categoryID, "error", err)
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
