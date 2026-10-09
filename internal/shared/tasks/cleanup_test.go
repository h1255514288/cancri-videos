package tasks

import (
 "context"
 "os"
 "path/filepath"
 "testing"

 "github.com/DATA-DOG/go-sqlmock"
 "github.com/jmoiron/sqlx"
)

func TestNodeDeletionRemovesFileBeforeCompletion(t *testing.T) {
 root:=t.TempDir();path:=filepath.Join(root,"video.mp4")
 if err:=os.WriteFile(path,[]byte("video"),0600);err!=nil {t.Fatal(err)}
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectBegin()
 m.ExpectQuery("SELECT dj.id").WithArgs(10,int64(3)).WillReturnRows(sqlmock.NewRows([]string{"id","video_id","storage_path","node_id"}).AddRow(1,2,"video.mp4",3))
 m.ExpectExec("UPDATE deletion_jobs").WithArgs(sqlmock.AnyArg(),int64(1)).WillReturnResult(sqlmock.NewResult(0,1))
 m.ExpectExec("UPDATE videos").WithArgs(sqlmock.AnyArg(),int64(2)).WillReturnResult(sqlmock.NewResult(0,1))
 m.ExpectCommit()
 s:=NewNodeCleanupService(sqlx.NewDb(db,"sqlmock"),nil,3,root)
 n,err:=s.ProcessDeletionJobs(context.Background(),10);if err!=nil || n!=1 {t.Fatalf("n=%d err=%v",n,err)}
 if _,err:=os.Stat(path);!os.IsNotExist(err) {t.Fatalf("file not deleted: %v",err)}
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}

func TestDeletionFailureKeepsInventoryAndSchedulesRetry(t *testing.T) {
 root:=t.TempDir()
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectBegin()
 m.ExpectQuery("SELECT dj.id").WithArgs(10,int64(3)).WillReturnRows(sqlmock.NewRows([]string{"id","video_id","storage_path","node_id"}).AddRow(1,2,"../unsafe",3))
 m.ExpectExec("UPDATE deletion_jobs SET retry_count=retry_count\\+1").WithArgs(sqlmock.AnyArg(),int64(1)).WillReturnResult(sqlmock.NewResult(0,1))
 m.ExpectCommit()
 n,err:=NewNodeCleanupService(sqlx.NewDb(db,"sqlmock"),nil,3,root).ProcessDeletionJobs(context.Background(),10)
 if err!=nil || n!=0 {t.Fatalf("n=%d err=%v",n,err)}
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}

func TestControlNeverDeletesNodeFiles(t *testing.T) {
 s:=NewCleanupService(nil,nil)
 n,err:=s.ProcessDeletionJobs(context.Background(),10)
 if n!=0 || err!=nil {t.Fatalf("n=%d err=%v",n,err)}
}

func TestExpiredClaimsRollbackWhenVideoReleaseFails(t *testing.T) {
 db,m,err:=sqlmock.New();if err!=nil {t.Fatal(err)};defer db.Close()
 m.ExpectBegin()
 m.ExpectExec("UPDATE claims").WithArgs("completed","reserved",sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0,1))
 m.ExpectExec("UPDATE videos").WithArgs("reserved","streaming","retryable",sqlmock.AnyArg()).WillReturnError(os.ErrPermission)
 m.ExpectRollback()
 s:=NewCleanupService(sqlx.NewDb(db,"sqlmock"),nil)
 if _,err:=s.ReleaseExpiredClaims(context.Background());err==nil {t.Fatal("expected rollback error")}
 if err:=m.ExpectationsWereMet();err!=nil {t.Fatal(err)}
}
