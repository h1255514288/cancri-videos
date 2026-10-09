package admin

import (
 "context"
 "errors"
 "net/http/httptest"
 "strings"
 "testing"
 "time"

 "github.com/DATA-DOG/go-sqlmock"
 "github.com/jmoiron/sqlx"
 "github.com/lib/pq"
)

func TestStrictManagementJSON(t *testing.T) {
 for _,test:=range []struct{body string;code int}{
 {`{"username":"user","password":"pass"}`,200},
 {`null`,400},{`[]`,400},{`{"extra":1}`,400},
 {`{"username":"x"} {}`,400},{`{"username":`,400},
 {`{"username":"`+strings.Repeat("x",4096)+`"}`,413},
 } {
  w:=httptest.NewRecorder();r:=httptest.NewRequest("POST","/admin/login",strings.NewReader(test.body))
  var req LoginRequest
  ok:=decodeRequest(w,r,&req)
  if w.Code!=test.code || ok!=(test.code==200) {t.Fatalf("code=%d ok=%v want=%d",w.Code,ok,test.code)}
 }
}

func TestInvalidManagementValuesNeverQueryDatabase(t *testing.T) {
 s:=NewService(nil,testSecret);negative:=-1;status:="unknown"
 if _,err:=s.CreateUser(context.Background(),CreateUserRequest{DailyQuota:&negative});!errors.Is(err,ErrInvalidRequest) {t.Fatal(err)}
 if _,err:=s.UpdateUser(context.Background(),1,UpdateUserRequest{Status:&status});!errors.Is(err,ErrInvalidRequest) {t.Fatal(err)}
 if _,err:=s.UpdateUser(context.Background(),1,UpdateUserRequest{});!errors.Is(err,ErrInvalidRequest) {t.Fatal(err)}
 expiry:=time.Now().Add(-time.Hour)
 if _,err:=s.CreateAPIKey(context.Background(),CreateAPIKeyRequest{UserID:1,ExpiresAt:&expiry});!errors.Is(err,ErrInvalidRequest) {t.Fatal(err)}
 if _,err:=s.CreateCategory(context.Background(),CreateCategoryRequest{Name:"  "});!errors.Is(err,ErrInvalidRequest) {t.Fatal(err)}
}

func TestLoginRejectsMalformedBodyWithoutDatabase(t *testing.T) {
 h:=NewHandler(NewService(nil,testSecret),nil)
 for _,body:=range []string{`null`,`{"username":"x","password":"y","extra":1}`,`{"username":"x"} {}`,`{"username":"x","password":"`+strings.Repeat("p",73)+`"}`} {
  w:=httptest.NewRecorder()
  h.ServeHTTP(w,httptest.NewRequest("POST","/admin/login",strings.NewReader(body)))
  if w.Code!=400 {t.Fatalf("got %d for invalid login",w.Code)}
 }
}

func TestDuplicateCategoryMapsToConflict(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectQuery("INSERT INTO categories").WithArgs("existing").WillReturnError(&pq.Error{Code:"23505"})
 _,err=NewService(sqlx.NewDb(db,"sqlmock"),testSecret).CreateCategory(context.Background(),CreateCategoryRequest{Name:"existing"})
 if !errors.Is(err,ErrCategoryExists) {t.Fatal(err)}
 w:=httptest.NewRecorder();if !writeServiceError(w,err) || w.Code!=409 {t.Fatal(w.Code)}
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}
