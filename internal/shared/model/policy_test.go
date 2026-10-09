package model

import (
 "errors"
 "testing"
 "time"
)

func TestDownloadLifecycle(t *testing.T){
 now:=time.Date(2026,1,1,0,0,0,0,time.UTC)
 key:=KeyState{Enabled:true}
 claim:=Claim{State:Reserved,ReservedUntil:now.Add(ReservationWindow)}
 claim,err:=claim.Start(now,key);if err!=nil{t.Fatal(err)}
 deadline:=claim.RetryUntil
 if _,err:=claim.Start(now,key);!errors.Is(err,ErrActive){t.Fatal("duplicate connection accepted")}
 claim,err=claim.Finish(now.Add(time.Minute),false);if err!=nil{t.Fatal(err)}
 if claim.State!=Retryable{t.Fatal(claim.State)}
 if _,err:=claim.Start(now.Add(time.Minute),key);!errors.Is(err,ErrRetryTooSoon){t.Fatal(err)}
 claim,err=claim.Start(now.Add(2*time.Minute),key);if err!=nil{t.Fatal(err)}
 if claim.RetryUntil!=deadline{t.Fatal("retry extended deadline")}
 claim,err=claim.Finish(now.Add(3*time.Minute),true);if err!=nil{t.Fatal(err)}
 if _,err:=claim.Start(now.Add(4*time.Minute),key);!errors.Is(err,ErrClaimClosed){t.Fatal("completed claim reused")}
}

func TestExpiryAndRecovery(t *testing.T){
 now:=time.Now().UTC()
 key:=KeyState{Enabled:true,ExpiresAt:now.Add(time.Minute)}
 c:=Claim{State:Reserved,ReservedUntil:now.Add(ReservationWindow)}
 expiry,err:=c.LinkExpiry(now,key);if err!=nil||!expiry.Equal(key.ExpiresAt){t.Fatalf("%v %v",expiry,err)}
 if _,err:=c.Start(key.ExpiresAt,key);!errors.Is(err,ErrKeyExpired){t.Fatal("expiry boundary accepted")}
 if c.Expire(c.ReservedUntil).State!=Released{t.Fatal("unstarted claim not released")}
 c,err=c.Start(now,key);if err!=nil{t.Fatal(err)}
 if c.Expire(now.Add(2*time.Hour)).State!=Streaming{t.Fatal("active connection expired")}
 recovered:=c.RecoverInterrupted()
 if recovered.State!=Uncertain || recovered.Expire(now.Add(2*time.Hour)).State!=Uncertain{t.Fatal("uncertain claim recycled")}
 key.Blacklisted=true
 c,err=c.Finish(now.Add(2*time.Minute),false);if err!=nil{t.Fatal(err)}
 if _,err:=c.Start(now.Add(3*time.Minute),key);!errors.Is(err,ErrKeyDisabled){t.Fatal("blacklisted retry accepted")}
 if c.Expire(c.RetryUntil).State!=Deleting{t.Fatal("started claim recycled")}
}

func TestRetryLimit(t *testing.T){
 now:=time.Now().UTC()
 c:=Claim{State:Reserved,ReservedUntil:now.Add(ReservationWindow)}
 for i:=0;i<MaxAttempts;i++{
  var err error
  start:=now.Add(time.Duration(i)*time.Minute)
  c,err=c.Start(start,KeyState{Enabled:true});if err!=nil{t.Fatal(err)}
  c,err=c.Finish(start.Add(time.Second),false);if err!=nil{t.Fatal(err)}
 }
 if c.State!=Deleting{t.Fatal("exhausted claim still retryable")}
}
