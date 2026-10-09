package admin

import (
 "context"
 "net/http/httptest"
 "strings"
 "testing"
 "time"

 "github.com/DATA-DOG/go-sqlmock"
 "github.com/jmoiron/sqlx"
 "github.com/yourorg/video-distribution-go/internal/shared/auth"
 "golang.org/x/crypto/bcrypt"
)

const testSecret="test-admin-secret-at-least-32-bytes"

func TestProtectedRoutesRejectUnauthenticated(t *testing.T) {
 h:=NewHandler(NewService(nil,testSecret),nil)
 for _,p:=range []string{"/admin/users","/admin/categories","/admin/api_keys","/admin/users/1/permissions/categories/2"} {
  w:=httptest.NewRecorder();h.ServeHTTP(w,httptest.NewRequest("POST",p,nil))
  if w.Code!=401 {t.Fatalf("%s: %d",p,w.Code)}
 }
 token,err:=auth.SignDownloadToken("clm_1","vid_1",1,1,time.Hour,testSecret);if err!=nil {t.Fatal(err)}
 r:=httptest.NewRequest("POST","/admin/users",nil);r.Header.Set("Authorization","Bearer "+token)
 w:=httptest.NewRecorder();h.ServeHTTP(w,r);if w.Code!=401 {t.Fatalf("download token accepted: %d",w.Code)}
}

func TestLoginUsesBcryptAndAdminAudience(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 s:=NewService(sqlx.NewDb(db,"sqlmock"),testSecret)
 hash,err:=bcrypt.GenerateFromPassword([]byte("correct-password"),bcrypt.MinCost);if err!=nil {t.Fatal(err)}
 for _,password:=range []string{"wrong","correct-password"} {
 m.ExpectQuery("SELECT id, username, password_hash, status FROM admins").WithArgs("operator").WillReturnRows(sqlmock.NewRows([]string{"id","username","password_hash","status"}).AddRow(7,"operator",string(hash),"active"))
 resp,err:=s.Login(context.Background(),LoginRequest{Username:"operator",Password:password})
 if password=="wrong" {if err!=ErrInvalidCredentials {t.Fatalf("wrong password: %v",err)};continue}
 if err!=nil {t.Fatal(err)}
 m.ExpectQuery("SELECT status FROM admins").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
 id,err:=s.authenticate(context.Background(),resp.Token);if err!=nil || id!=7 {t.Fatalf("id=%d err=%v",id,err)}
 }
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}

func TestCreateAPIKeyInsertsOnce(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 s:=NewService(sqlx.NewDb(db,"sqlmock"),testSecret)
 m.ExpectQuery("SELECT EXISTS").WithArgs(int64(4)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
 m.ExpectQuery("INSERT INTO api_keys").WithArgs(int64(4),sqlmock.AnyArg(),sqlmock.AnyArg(),nil,nil).WillReturnRows(sqlmock.NewRows([]string{"id","created_at"}).AddRow(8,time.Now()))
 resp,err:=s.CreateAPIKey(context.Background(),CreateAPIKeyRequest{UserID:4});if err!=nil {t.Fatal(err)}
 if resp.KeyID!=8 || !strings.HasPrefix(resp.APIKey,"vds_") {t.Fatalf("invalid response: %+v",resp)}
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}
