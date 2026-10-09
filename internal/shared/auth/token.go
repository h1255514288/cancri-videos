package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidToken = errors.New("invalid_token")
	ErrExpiredToken = errors.New("expired_token")
)

type DownloadClaims struct {
	ClaimID           string `json:"claim_id"`
	VideoID           string `json:"video_id"`
	NodeID            int64  `json:"node_id"`
	CredentialVersion int    `json:"credential_version"`
	jwt.RegisteredClaims
}

func SignDownloadToken(claimID string, videoID string, nodeID int64, credVersion int, ttl time.Duration, secret string) (string, error) {
	return SignDownloadTokenUntil(claimID, videoID, nodeID, credVersion, time.Now().UTC().Add(ttl), secret)
}

func SignDownloadTokenUntil(claimID string, videoID string, nodeID int64, credVersion int, expiresAt time.Time, secret string) (string, error) {
	now := time.Now().UTC()
	if claimID == "" || videoID == "" || nodeID <= 0 || credVersion <= 0 || secret == "" || !now.Before(expiresAt.Truncate(time.Second)) {
		return "", ErrInvalidToken
	}
	claims := DownloadClaims{
		ClaimID:           claimID,
		VideoID:           videoID,
		NodeID:            nodeID,
		CredentialVersion: credVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func VerifyDownloadToken(tokenString string, secret string) (*DownloadClaims, error) {
	if secret == "" {
		return nil, ErrInvalidToken
	}
	token, err := jwt.ParseWithClaims(tokenString, &DownloadClaims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	claims, ok := token.Claims.(*DownloadClaims)
	if !ok || !token.Valid || claims.ClaimID == "" || claims.VideoID == "" || claims.NodeID <= 0 || claims.CredentialVersion <= 0 {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func HashAPIKey(apiKey string) string {
	h := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(h[:])
}

func ExtractPrefix(apiKey string) string {
	if len(apiKey) < 10 {
		return apiKey
	}
	return apiKey[:10]
}
