package model

import "time"

type Video struct {
 ID          int64     `db:"id"`
 VideoID     string    `db:"video_id"`
 CategoryID  int64     `db:"category_id"`
 NodeID      int64     `db:"node_id"`
 Title       string    `db:"title"`
 Filename    string    `db:"filename"`
 StoragePath string    `db:"storage_path"`
 SizeBytes   int64     `db:"size_bytes"`
 SHA256      string    `db:"sha256"`
 MimeType    string    `db:"mime_type"`
 Status      string    `db:"status"`
 UploadedBy  *int64    `db:"uploaded_by"`
 UploadedAt  time.Time `db:"uploaded_at"`
 ClaimedAt   *time.Time `db:"claimed_at"`
 FirstDownloadAt *time.Time `db:"first_download_at"`
 ConsumedAt  *time.Time `db:"consumed_at"`
 DeletedAt   *time.Time `db:"deleted_at"`
 ErrorMessage *string  `db:"error_message"`
}

type ClaimRecord struct {
 ID              int64     `db:"id"`
 ClaimID         string    `db:"claim_id"`
 UserID          int64     `db:"user_id"`
 VideoID         int64     `db:"video_id"`
 IdempotencyKey  string    `db:"idempotency_key"`
 Status          string    `db:"status"`
 ClaimedAt       time.Time `db:"claimed_at"`
 ClaimExpiresAt  time.Time `db:"claim_expires_at"`
 FirstDownloadAt *time.Time `db:"first_download_at"`
 RetryDeadline   *time.Time `db:"retry_deadline"`
 CompletedAt     *time.Time `db:"completed_at"`
 CredentialVersion int     `db:"credential_version"`
 RetryCount      int       `db:"retry_count"`
}

type User struct {
 ID                      int64      `db:"id"`
 GroupID                 *int64     `db:"group_id"`
 InternalName            *string    `db:"internal_name"`
 Status                  string     `db:"status"`
 BlacklistReason         *string    `db:"blacklist_reason"`
 BlacklistedAt           *time.Time `db:"blacklisted_at"`
 BlacklistedBy           *int64     `db:"blacklisted_by"`
 DailyQuota              *int       `db:"daily_quota"`
 MaxPendingClaims        *int       `db:"max_pending_claims"`
 MaxConcurrentDownloads  *int       `db:"max_concurrent_downloads"`
 DownloadSpeedLimitMbps  *int       `db:"download_speed_limit_mbps"`
 TotalClaims             int        `db:"total_claims"`
 LastClaimAt             *time.Time `db:"last_claim_at"`
 CreatedAt               time.Time  `db:"created_at"`
 UpdatedAt               time.Time  `db:"updated_at"`
}

type APIKey struct {
 ID          int64      `db:"id"`
 UserID      int64      `db:"user_id"`
 KeyHash     string     `db:"key_hash"`
 KeyPrefix   string     `db:"key_prefix"`
 Status      string     `db:"status"`
 EffectiveAt *time.Time `db:"effective_at"`
 ExpiresAt   *time.Time `db:"expires_at"`
 RevokedAt   *time.Time `db:"revoked_at"`
 RevokedBy   *int64     `db:"revoked_by"`
 RevokeReason *string   `db:"revoke_reason"`
 LastUsedAt  *time.Time `db:"last_used_at"`
 CreatedAt   time.Time  `db:"created_at"`
}

type Category struct {
 ID        int64     `db:"id"`
 Name      string    `db:"name"`
 Status    string    `db:"status"`
 CreatedAt time.Time `db:"created_at"`
 UpdatedAt time.Time `db:"updated_at"`
}

type DailyQuota struct {
 ID          int64      `db:"id"`
 UserID      int64      `db:"user_id"`
 QuotaDate   time.Time  `db:"quota_date"`
 UsedCount   int        `db:"used_count"`
 LastClaimAt *time.Time `db:"last_claim_at"`
}

type DownloadAttempt struct {
 ID                int64      `db:"id"`
 AttemptID         string     `db:"attempt_id"`
 ClaimID           int64      `db:"claim_id"`
 NodeID            int64      `db:"node_id"`
 CredentialVersion int        `db:"credential_version"`
 StartedAt         time.Time  `db:"started_at"`
 EndedAt           *time.Time `db:"ended_at"`
 BytesSent         int64      `db:"bytes_sent"`
 Result            *string    `db:"result"`
 ErrorMessage      *string    `db:"error_message"`
 ClientIP          *string    `db:"client_ip"`
 UserAgent         *string    `db:"user_agent"`
}
