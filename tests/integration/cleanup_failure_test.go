//go:build integration

package integration

import (
 "context"
 "testing"

 "github.com/yourorg/video-distribution-go/internal/shared/tasks"
)

func TestFailedDeletionJobCannotResetRetryBudget(t *testing.T) {
 db:=fixture(t,1,10,3)
 exec(t,db,`UPDATE videos SET status='pending_deletion'`)
 exec(t,db,`INSERT INTO deletion_jobs(video_id,node_id,storage_path,status,retry_count,max_retries) SELECT id,node_id,storage_path,'failed',5,5 FROM videos`)
 n,err:=tasks.NewCleanupService(db,nil).CreateDeletionJobs(context.Background())
 if err!=nil {t.Fatal(err)}
 if n!=0 || scalar(t,db,`SELECT COUNT(*) FROM deletion_jobs`)!=1 {t.Fatal("failed job retry budget was reset by new job")}
}
