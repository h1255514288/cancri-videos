package download

import (
 "context"
 "net/http/httptest"
 "os"
 "path/filepath"
 "testing"
 "time"

 "github.com/DATA-DOG/go-sqlmock"
 "github.com/jmoiron/sqlx"
 "github.com/yourorg/video-distribution-go/internal/shared/auth"
)

func TestStreamingClaimRejectsSecondDownload(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectBegin()
 m.ExpectQuery("SELECT ").WithArgs("clm_active").WillReturnRows(sqlmock.NewRows([]string{"id","claim_id","status"}).AddRow(1,"clm_active","streaming"))
 m.ExpectRollback()
 h:=NewHandler(sqlx.NewDb(db,"sqlmock"),"secret",nil)
 err=h.handleDownload(context.Background(),httptest.NewRecorder(),httptest.NewRequest("GET","/d/token",nil),&auth.DownloadClaims{ClaimID:"clm_active"})
 if err!=ErrDownloadActive {t.Fatalf("got %v",err)}
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}

func TestStreamingRecoveryRejectsExpiredWindow(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectBegin()
 m.ExpectQuery("SELECT ").WithArgs("clm_expired").WillReturnRows(sqlmock.NewRows([]string{"id","claim_id","status","retry_count","retry_deadline"}).AddRow(1,"clm_expired","streaming",1,time.Now().Add(-time.Second)))
 m.ExpectRollback()
 h:=NewHandler(sqlx.NewDb(db,"sqlmock"),"secret",nil,2)
 err=h.handleDownload(context.Background(),httptest.NewRecorder(),httptest.NewRequest("GET","/d/token",nil),&auth.DownloadClaims{ClaimID:"clm_expired"})
 if err!=ErrClaimExpired {t.Fatalf("got %v",err)}
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}

func TestStoragePathConfinement(t *testing.T) {
 root:=t.TempDir()
 if err:=os.WriteFile(filepath.Join(root,"video.mp4"),[]byte("video"),0600);err!=nil {t.Fatal(err)}
 if _,err:=safeFilePath(root,"video.mp4");err!=nil {t.Fatal(err)}
 for _,name:=range []string{"../outside.mp4",filepath.Join(root,"video.mp4"),""} {
 if _,err:=safeFilePath(root,name);err==nil {t.Fatalf("accepted %q",name)}
 }
}

func TestExpiredAdminStyleTokenRejected(t *testing.T) {
 _,err:=auth.SignDownloadToken("clm","vid",1,1,-time.Hour,"secret")
 if err==nil {t.Fatal("expired token signed")}
}
