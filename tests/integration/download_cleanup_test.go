//go:build integration

package integration

import (
 "context"
 "crypto/sha256"
 "fmt"
 "io"
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "testing"
 "time"

 "github.com/gofrs/flock"
 "github.com/yourorg/video-distribution-go/internal/control/claim"
 "github.com/yourorg/video-distribution-go/internal/node/download"
 "github.com/yourorg/video-distribution-go/internal/shared/auth"
 "github.com/yourorg/video-distribution-go/internal/shared/tasks"
)

func TestRealDownloadAndDeletion(t *testing.T) {
 db:=fixture(t,1,10,3)
 root:=t.TempDir();content:=[]byte("actual video fixture bytes")
 path:=filepath.Join(root,"video.mp4")
 if err:=os.WriteFile(path,content,0600);err!=nil {t.Fatal(err)}
 exec(t,db,`UPDATE storage_nodes SET storage_root_path=$1 WHERE id=2`,root)
 exec(t,db,`UPDATE videos SET storage_path='video.mp4',size_bytes=$1`,len(content))
 result,err:=claim.NewService(db).Claim(context.Background(),7,3,"download-test");if err!=nil {t.Fatal(err)}
 secret:="integration-secret-more-than-32-bytes"
 token,err:=auth.SignDownloadToken(result.Claim.ClaimID,result.Video.VideoID,2,1,time.Minute,secret);if err!=nil {t.Fatal(err)}
 srv:=httptest.NewServer(download.NewHandler(db,secret,nil));defer srv.Close()
 resp,err:=http.Get(srv.URL+"/d/"+token);if err!=nil {t.Fatal(err)}
 bytes,err:=io.ReadAll(resp.Body);resp.Body.Close();if err!=nil || resp.StatusCode!=200 || string(bytes)!=string(content) {t.Fatalf("download status=%d err=%v",resp.StatusCode,err)}
 // Completion is synchronous with the handler, but the client may receive the body before commit.
 deadline:=time.Now().Add(5*time.Second)
 for scalar(t,db,`SELECT COUNT(*) FROM claims WHERE status='completed'`)==0 && time.Now().Before(deadline) {time.Sleep(10*time.Millisecond)}
 if scalar(t,db,`SELECT COUNT(*) FROM videos WHERE status='consumed'`)!=1 {t.Fatal("video not consumed")}
 resp,err=http.Get(srv.URL+"/d/"+token);if err!=nil {t.Fatal(err)};resp.Body.Close()
 if resp.StatusCode!=410 {t.Fatalf("repeat download accepted: %d",resp.StatusCode)}
 exec(t,db,`UPDATE claims SET completed_at=NOW()-INTERVAL '2 hours'`)
 cleanup:=tasks.NewNodeCleanupService(db,nil,2,root)
 if _,err:=cleanup.MarkVideosForDeletion(context.Background());err!=nil {t.Fatal(err)}
 if _,err:=cleanup.CreateDeletionJobs(context.Background());err!=nil {t.Fatal(err)}
 n,err:=cleanup.ProcessDeletionJobs(context.Background(),10);if err!=nil || n!=1 {t.Fatalf("deletion n=%d err=%v",n,err)}
 if _,err:=os.Stat(path);!os.IsNotExist(err) {t.Fatalf("file remains: %v",err)}
 if scalar(t,db,`SELECT COUNT(*) FROM videos WHERE status='deleted'`)!=1 {t.Fatal("video not deleted")}
}

func TestStreamingRecoveryRespectsLiveLock(t *testing.T) {
 db:=fixture(t,1,10,3);root:=t.TempDir()
 content:=[]byte("recovered video")
 if err:=os.WriteFile(filepath.Join(root,"video.mp4"),content,0600);err!=nil {t.Fatal(err)}
 exec(t,db,`UPDATE storage_nodes SET storage_root_path=$1 WHERE id=2`,root)
 exec(t,db,`UPDATE videos SET storage_path='video.mp4',size_bytes=$1`,len(content))
 result,err:=claim.NewService(db).Claim(context.Background(),7,3,"recover-test");if err!=nil {t.Fatal(err)}
 exec(t,db,`UPDATE claims SET status='streaming',first_download_at=NOW(),retry_deadline=NOW()+INTERVAL '1 hour',retry_count=1`)
 secret:="integration-secret-more-than-32-bytes"
 token,err:=auth.SignDownloadToken(result.Claim.ClaimID,result.Video.VideoID,2,1,time.Minute,secret);if err!=nil {t.Fatal(err)}
 dir:=filepath.Join(root,".download-locks");if err:=os.MkdirAll(dir,0700);err!=nil {t.Fatal(err)}
 key:=sha256.Sum256([]byte(result.Claim.ClaimID))
 lock:=flock.New(filepath.Join(dir,fmt.Sprintf("%x.lock",key)))
 if err:=lock.Lock();err!=nil {t.Fatal(err)};defer lock.Close()
 srv:=httptest.NewServer(download.NewHandler(db,secret,nil,2));defer srv.Close()
 resp,err:=http.Get(srv.URL+"/d/"+token);if err!=nil {t.Fatal(err)};resp.Body.Close()
 if resp.StatusCode!=409 {t.Fatalf("active lock bypassed: %d",resp.StatusCode)}
 if err:=lock.Close();err!=nil {t.Fatal(err)}
 resp,err=http.Get(srv.URL+"/d/"+token);if err!=nil {t.Fatal(err)}
 data,err:=io.ReadAll(resp.Body);resp.Body.Close()
 if err!=nil || resp.StatusCode!=200 || string(data)!=string(content) {t.Fatalf("recovery: %d %v",resp.StatusCode,err)}
 deadline:=time.Now().Add(5*time.Second)
 for scalar(t,db,`SELECT COUNT(*) FROM claims WHERE status='completed' AND retry_count=2`)==0 && time.Now().Before(deadline) {time.Sleep(10*time.Millisecond)}
 if scalar(t,db,`SELECT COUNT(*) FROM claims WHERE status='completed' AND retry_count=2`)!=1 {t.Fatal("recovery attempt not committed")}
}

func TestExpiredReservationReturnsInventory(t *testing.T) {
 db:=fixture(t,1,10,3)
 result,err:=claim.NewService(db).Claim(context.Background(),7,3,"expired-test");if err!=nil {t.Fatal(err)}
 exec(t,db,`UPDATE claims SET claim_expires_at=NOW()-INTERVAL '1 second' WHERE claim_id=$1`,result.Claim.ClaimID)
 n,err:=tasks.NewCleanupService(db,nil).ReleaseExpiredClaims(context.Background());if err!=nil || n!=1 {t.Fatalf("n=%d err=%v",n,err)}
 if scalar(t,db,`SELECT COUNT(*) FROM videos WHERE status='available'`)!=1 {t.Fatal("inventory not restored")}
 if _,err:=claim.NewService(db).Claim(context.Background(),8,3,"new-owner");err!=nil {t.Fatal(err)}
}
