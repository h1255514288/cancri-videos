package model

import (
 "errors"
 "time"
)

const (
 ReservationWindow = 10*time.Minute
 RetryWindow = time.Hour
 LinkTTL = 5*time.Minute
 RetryInterval = 30*time.Second
 MaxAttempts = 6
)

var (
 ErrKeyDisabled = errors.New("key_disabled")
 ErrKeyExpired = errors.New("key_expired")
 ErrKeyNotEffective = errors.New("key_not_effective")
 ErrClaimClosed = errors.New("claim_closed")
 ErrExpired = errors.New("claim_window_expired")
 ErrActive = errors.New("download_already_active")
 ErrRetryLimit = errors.New("retry_limit_reached")
 ErrRetryTooSoon = errors.New("retry_too_soon")
)

type KeyState struct {
 Enabled bool
 Blacklisted bool
 EffectiveAt time.Time
 ExpiresAt time.Time
}

func (k KeyState) Authorize(now time.Time) error {
 if !k.Enabled || k.Blacklisted { return ErrKeyDisabled }
 if !k.EffectiveAt.IsZero() && now.Before(k.EffectiveAt) { return ErrKeyNotEffective }
 if !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) { return ErrKeyExpired }
 return nil
}

type ClaimState string

const (
 Reserved ClaimState = "reserved"
 Streaming ClaimState = "streaming"
 Retryable ClaimState = "retryable"
 Uncertain ClaimState = "uncertain"
 Completed ClaimState = "completed"
 Released ClaimState = "released"
 Deleting ClaimState = "deleting"
 Deleted ClaimState = "deleted"
)

type Claim struct {
 State ClaimState
 ReservedUntil time.Time
 FirstStartedAt time.Time
 RetryUntil time.Time
 LastEndedAt time.Time
 Attempts int
 ActiveAttempt bool
}

func (c Claim) Deadline() time.Time {
 if c.FirstStartedAt.IsZero() { return c.ReservedUntil }
 return c.RetryUntil
}

func (c Claim) LinkExpiry(now time.Time, key KeyState) (time.Time,error) {
 if err:=key.Authorize(now);err!=nil{return time.Time{},err}
 if c.State!=Reserved && c.State!=Retryable {return time.Time{},ErrClaimClosed}
 deadline:=c.Deadline()
 if deadline.IsZero() || !now.Before(deadline) {return time.Time{},ErrExpired}
 expiry:=now.Add(LinkTTL)
 if deadline.Before(expiry){expiry=deadline}
 if !key.ExpiresAt.IsZero() && key.ExpiresAt.Before(expiry){expiry=key.ExpiresAt}
 return expiry,nil
}

func (c Claim) Start(now time.Time,key KeyState)(Claim,error){
 if c.ActiveAttempt || c.State==Streaming{return c,ErrActive}
 if _,err:=c.LinkExpiry(now,key);err!=nil{return c,err}
 if c.Attempts>=MaxAttempts{return c,ErrRetryLimit}
 if !c.LastEndedAt.IsZero() && now.Before(c.LastEndedAt.Add(RetryInterval)){return c,ErrRetryTooSoon}
 if c.FirstStartedAt.IsZero(){c.FirstStartedAt=now;c.RetryUntil=now.Add(RetryWindow)}
 c.Attempts++
 c.ActiveAttempt=true
 c.State=Streaming
 return c,nil
}

func (c Claim) Finish(now time.Time,complete bool)(Claim,error){
 if c.State!=Streaming || !c.ActiveAttempt{return c,ErrClaimClosed}
 c.ActiveAttempt=false
 c.LastEndedAt=now
 if complete{c.State=Completed}else if !now.Before(c.RetryUntil) || c.Attempts>=MaxAttempts{c.State=Deleting}else{c.State=Retryable}
 return c,nil
}

func (c Claim) Expire(now time.Time) Claim {
 if c.ActiveAttempt || c.State==Streaming || c.State==Uncertain{return c}
 if c.State==Reserved && !now.Before(c.ReservedUntil){c.State=Released}
 if c.State==Retryable && !now.Before(c.RetryUntil){c.State=Deleting}
 return c
}

func (c Claim) RecoverInterrupted() Claim {
 if c.ActiveAttempt || c.State==Streaming{c.State=Uncertain}
 return c
}
