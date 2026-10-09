package download

import (
 "context"
 "testing"
 "time"

 "github.com/DATA-DOG/go-sqlmock"
 "github.com/jmoiron/sqlx"
)

func TestInterruptedDownloadWindowTransitions(t *testing.T) {
 for _,scenario:=range []string{"valid","expired","missing"} {
 t.Run(scenario,func(t *testing.T){
  db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
  var deadline interface{}
  if scenario=="valid" {deadline=time.Now().Add(time.Hour)}
  if scenario=="expired" {deadline=time.Now().Add(-time.Second)}
  m.ExpectBegin()
  m.ExpectQuery("SELECT ").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id","claim_id","video_id","status","retry_count","retry_deadline"}).AddRow(1,"clm_interrupted",2,"streaming",1,deadline))
  if scenario=="valid" {
   m.ExpectExec(`UPDATE claims SET status=\$1 WHERE id=\$2`).WithArgs("retryable",int64(1)).WillReturnResult(sqlmock.NewResult(0,1))
  } else {
   m.ExpectExec(`UPDATE claims SET status=\$1, completed_at=\$2 WHERE id=\$3`).WithArgs("deleting",sqlmock.AnyArg(),int64(1)).WillReturnResult(sqlmock.NewResult(0,1))
  }
  m.ExpectCommit()
  NewHandler(sqlx.NewDb(db,"sqlmock"),"secret",nil).updateDownloadStatus(context.Background(),1,"clm_interrupted",false,128,1)
  if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
 })
 }
}

func TestCompletionRejectsMissingAttemptIdentity(t *testing.T) {
 NewHandler(nil,"secret",nil).updateDownloadStatus(context.Background(),1,"clm_invalid",true,1024,0)
}

func TestOldAttemptCannotCompleteNewStreamingAttempt(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectBegin()
 m.ExpectQuery("SELECT ").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id","claim_id","status","retry_count"}).AddRow(1,"clm_new_attempt","streaming",2))
 m.ExpectRollback()
 NewHandler(sqlx.NewDb(db,"sqlmock"),"secret",nil).updateDownloadStatus(context.Background(),1,"clm_new_attempt",true,1024,1)
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}

func TestLateCompletionCannotOverwriteClosedClaim(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectBegin()
 m.ExpectQuery("SELECT ").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id","claim_id","status"}).AddRow(1,"clm_closed","deleting"))
 m.ExpectRollback()
 NewHandler(sqlx.NewDb(db,"sqlmock"),"secret",nil).updateDownloadStatus(context.Background(),1,"clm_closed",true,1024,1)
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}
