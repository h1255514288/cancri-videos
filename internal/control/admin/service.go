package admin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/yourorg/video-distribution-go/internal/shared/auth"
	"github.com/yourorg/video-distribution-go/internal/shared/model"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidRequest = errors.New("invalid_request")
	ErrKeyNotFound = errors.New("key_not_found_or_already_revoked")
	ErrInvalidCredentials = errors.New("invalid_credentials")
	ErrUserNotFound       = errors.New("user_not_found")
	ErrUserExists         = errors.New("user_already_exists")
	ErrCategoryNotFound   = errors.New("category_not_found")
	ErrCategoryExists     = errors.New("category_already_exists")
)

type Service struct {
	db *sqlx.DB
	secret string
}

func NewService(database *sqlx.DB, secret string) *Service {
	return &Service{db: database, secret: secret}
}

// Admin Authentication

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	AdminID   int64     `json:"admin_id"`
	Username  string    `json:"username"`
}

func (s *Service) Login(ctx context.Context, req LoginRequest) (*LoginResponse, error) {
	if strings.TrimSpace(req.Username) == "" || utf8.RuneCountInString(req.Username) > 64 || req.Password == "" || len(req.Password) > 72 { return nil, ErrInvalidRequest }
	var admin struct {
		ID           int64  `db:"id"`
		Username     string `db:"username"`
		PasswordHash string `db:"password_hash"`
		Status       string `db:"status"`
	}

	err := s.db.GetContext(ctx, &admin, `SELECT id, username, password_hash, status FROM admins WHERE username=$1`, req.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("query admin: %w", err)
	}

	if admin.Status != "active" {
		return nil, ErrInvalidCredentials
	}

	if bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)) != nil {
		return nil, ErrInvalidCredentials
	}

	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	token, err := s.signAdminToken(admin.ID, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("sign token: %w", err)
	}

	return &LoginResponse{
		Token:     token,
		ExpiresAt: expiresAt,
		AdminID:   admin.ID,
		Username:  admin.Username,
	}, nil
}

// User Management

type CreateUserRequest struct {
	GroupID      *int64  `json:"group_id"`
	InternalName *string `json:"internal_name"`
	DailyQuota   *int    `json:"daily_quota"`
}

type UserResponse struct {
	ID           int64      `json:"id"`
	GroupID      *int64     `json:"group_id"`
	InternalName *string    `json:"internal_name"`
	Status       string     `json:"status"`
	DailyQuota   *int       `json:"daily_quota"`
	TotalClaims  int        `json:"total_claims"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) (*UserResponse, error) {
	if (req.GroupID != nil && *req.GroupID <= 0) || (req.DailyQuota != nil && *req.DailyQuota < 0) || (req.InternalName != nil && utf8.RuneCountInString(*req.InternalName) > 128) { return nil, ErrInvalidRequest }
	var user model.User
	err := s.db.GetContext(ctx, &user,
		`INSERT INTO users (group_id, internal_name, status, daily_quota, created_at, updated_at)
		VALUES ($1, $2, 'active', $3, NOW(), NOW())
		RETURNING id, group_id, internal_name, status, daily_quota, total_claims, created_at, updated_at`,
		req.GroupID, req.InternalName, req.DailyQuota)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	return &UserResponse{
		ID:           user.ID,
		GroupID:      user.GroupID,
		InternalName: user.InternalName,
		Status:       user.Status,
		DailyQuota:   user.DailyQuota,
		TotalClaims:  user.TotalClaims,
		CreatedAt:    user.CreatedAt,
		UpdatedAt:    user.UpdatedAt,
	}, nil
}

func (s *Service) GetUser(ctx context.Context, userID int64) (*UserResponse, error) {
	var user model.User
	err := s.db.GetContext(ctx, &user, `SELECT * FROM users WHERE id=$1`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}

	return &UserResponse{
		ID:           user.ID,
		GroupID:      user.GroupID,
		InternalName: user.InternalName,
		Status:       user.Status,
		DailyQuota:   user.DailyQuota,
		TotalClaims:  user.TotalClaims,
		CreatedAt:    user.CreatedAt,
		UpdatedAt:    user.UpdatedAt,
	}, nil
}

type UpdateUserRequest struct {
	Status     *string `json:"status"`
	DailyQuota *int    `json:"daily_quota"`
}

func (s *Service) UpdateUser(ctx context.Context, userID int64, req UpdateUserRequest) (*UserResponse, error) {
	if userID <= 0 || (req.Status == nil && req.DailyQuota == nil) || (req.DailyQuota != nil && *req.DailyQuota < 0) { return nil, ErrInvalidRequest }
	if req.Status != nil && *req.Status != "active" && *req.Status != "disabled" && *req.Status != "blacklisted" { return nil, ErrInvalidRequest }
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var user model.User
	err = tx.GetContext(ctx, &user, `SELECT * FROM users WHERE id=$1 FOR UPDATE`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock user: %w", err)
	}

	if req.Status != nil {
		user.Status = *req.Status
	}
	if req.DailyQuota != nil {
		user.DailyQuota = req.DailyQuota
	}
	user.UpdatedAt = time.Now().UTC()

	_, err = tx.ExecContext(ctx, `UPDATE users SET status=$1, daily_quota=$2, updated_at=$3 WHERE id=$4`,
		user.Status, user.DailyQuota, user.UpdatedAt, user.ID)
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &UserResponse{
		ID:           user.ID,
		GroupID:      user.GroupID,
		InternalName: user.InternalName,
		Status:       user.Status,
		DailyQuota:   user.DailyQuota,
		TotalClaims:  user.TotalClaims,
		CreatedAt:    user.CreatedAt,
		UpdatedAt:    user.UpdatedAt,
	}, nil
}

// API Key Management

type CreateAPIKeyRequest struct {
	UserID      int64      `json:"user_id"`
	EffectiveAt *time.Time `json:"effective_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

type APIKeyResponse struct {
	KeyID     int64      `json:"key_id"`
	APIKey    string     `json:"api_key,omitempty"` // Only returned on creation
	KeyPrefix string     `json:"key_prefix"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil { return "", err }
	return "vds_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)[:40], nil
}

func (s *Service) CreateAPIKey(ctx context.Context, req CreateAPIKeyRequest) (*APIKeyResponse, error) {
	if req.UserID <= 0 || (req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now())) || (req.ExpiresAt != nil && req.EffectiveAt != nil && !req.ExpiresAt.After(*req.EffectiveAt)) { return nil, ErrInvalidRequest }
	// Check user exists
	var userExists bool
	err := s.db.GetContext(ctx, &userExists, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("check user: %w", err)
	}
	if !userExists {
		return nil, ErrUserNotFound
	}

	apiKey, err := generateAPIKey()
	if err != nil { return nil, fmt.Errorf("generate key: %w", err) }
	keyHash := auth.HashAPIKey(apiKey)
	keyPrefix := auth.ExtractPrefix(apiKey)

	var result struct {
		ID        int64     `db:"id"`
		CreatedAt time.Time `db:"created_at"`
	}
	err = s.db.GetContext(ctx, &result,
		`INSERT INTO api_keys (user_id, key_hash, key_prefix, status, effective_at, expires_at, created_at)
		VALUES ($1, $2, $3, 'active', $4, $5, NOW())
		RETURNING id, created_at`,
		req.UserID, keyHash, keyPrefix, req.EffectiveAt, req.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("insert api_key: %w", err)
	}

	return &APIKeyResponse{
		KeyID:     result.ID,
		APIKey:    apiKey,
		KeyPrefix: keyPrefix,
		Status:    "active",
		CreatedAt: result.CreatedAt,
		ExpiresAt: req.ExpiresAt,
	}, nil
}

func (s *Service) RevokeAPIKey(ctx context.Context, keyID int64, adminID int64, reason string) error {
	if keyID <= 0 || adminID <= 0 || len(reason) > 4096 { return ErrInvalidRequest }
	result, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET status='revoked', revoked_at=NOW(), revoked_by=$1, revoke_reason=$2 WHERE id=$3 AND status='active'`,
		adminID, reason, keyID)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil { return err }
	if rows == 0 {
		return ErrKeyNotFound
	}

	return nil
}

// Category Management

type CreateCategoryRequest struct {
	Name string `json:"name"`
}

type CategoryResponse struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Service) CreateCategory(ctx context.Context, req CreateCategoryRequest) (*CategoryResponse, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 128 { return nil, ErrInvalidRequest }
	var category model.Category
	err := s.db.GetContext(ctx, &category,
		`INSERT INTO categories (name, status, created_at, updated_at) VALUES ($1, 'enabled', NOW(), NOW())
		RETURNING id, name, status, created_at`,
		req.Name)
	if err != nil {
		var constraint *pq.Error
		if errors.As(err, &constraint) && constraint.Code == "23505" {
			return nil, ErrCategoryExists
		}
		return nil, fmt.Errorf("insert category: %w", err)
	}

	return &CategoryResponse{
		ID:        category.ID,
		Name:      category.Name,
		Status:    category.Status,
		CreatedAt: category.CreatedAt,
	}, nil
}

func (s *Service) ListCategories(ctx context.Context) ([]CategoryResponse, error) {
	var categories []model.Category
	err := s.db.SelectContext(ctx, &categories, `SELECT id, name, status, created_at FROM categories ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}

	result := make([]CategoryResponse, len(categories))
	for i, c := range categories {
		result[i] = CategoryResponse{
			ID:        c.ID,
			Name:      c.Name,
			Status:    c.Status,
			CreatedAt: c.CreatedAt,
		}
	}
	return result, nil
}

// Grant user permission to category
func (s *Service) GrantCategoryPermission(ctx context.Context, userID, categoryID, adminID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_category_permissions (user_id, category_id, granted_at, granted_by)
		VALUES ($1, $2, NOW(), $3)
		ON CONFLICT (user_id, category_id) DO NOTHING`,
		userID, categoryID, adminID)
	if err != nil {
		return fmt.Errorf("grant permission: %w", err)
	}
	return nil
}
