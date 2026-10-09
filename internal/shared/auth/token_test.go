package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestDownloadToken(t *testing.T) {
	secret := "unit-test-signing-key"
	expiry := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	token, err := SignDownloadTokenUntil("clm_1", "vid_1", 2, 3, expiry, secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyDownloadToken(token, secret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ClaimID != "clm_1" || claims.VideoID != "vid_1" || claims.NodeID != 2 || claims.CredentialVersion != 3 || !claims.ExpiresAt.Time.Equal(expiry) {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if _, err := VerifyDownloadToken(token, "wrong-key"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if _, err := VerifyDownloadToken(token, ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if _, err := VerifyDownloadToken("malformed", secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if _, err := SignDownloadToken("clm_1", "vid_1", 2, 3, -time.Minute, secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expired token signed")
	}
	if _, err := SignDownloadToken("clm_1", "vid_1", 2, 3, time.Minute, secret); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadTokenRejectsInvalidClaims(t *testing.T) {
	for _, name := range []string{"expired", "no_expiry", "hs384", "missing_id", "invalid_node", "invalid_version"} {
		t.Run(name, func(t *testing.T) {
			claims := DownloadClaims{ClaimID: "clm_1", VideoID: "vid_1", NodeID: 1, CredentialVersion: 1,
				RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
			method := jwt.SigningMethodHS256
			want := ErrInvalidToken
			switch name {
			case "expired":
				claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
				want = ErrExpiredToken
			case "no_expiry":
				claims.ExpiresAt = nil
			case "hs384":
				method = jwt.SigningMethodHS384
			case "missing_id":
				claims.ClaimID = ""
			case "invalid_node":
				claims.NodeID = 0
			case "invalid_version":
				claims.CredentialVersion = 0
			}
			token, err := jwt.NewWithClaims(method, claims).SignedString([]byte("test-key"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyDownloadToken(token, "test-key"); !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
		})
	}
}
