package download

import (
 "bufio"
 "fmt"
 "os"
 "os/exec"
 "testing"
 "time"
)

func TestClaimLockProcessHelper(t *testing.T) {
 if os.Getenv("VDS_LOCK_HELPER") != "1" { return }
 unlock,err:=acquireClaimLock(os.Getenv("VDS_LOCK_ROOT"),"clm_crash")
 if err!=nil {os.Exit(2)}
 defer unlock()
 fmt.Println("locked")
 for { time.Sleep(time.Hour) }
}

func TestClaimLockReleasedByProcessDeath(t *testing.T) {
 root:=t.TempDir()
 cmd:=exec.Command(os.Args[0],"-test.run=^TestClaimLockProcessHelper$")
 cmd.Env=append(os.Environ(),"VDS_LOCK_HELPER=1","VDS_LOCK_ROOT="+root)
 stdout,err:=cmd.StdoutPipe();if err!=nil {t.Fatal(err)}
 if err:=cmd.Start();err!=nil {t.Fatal(err)}
 defer func(){cmd.Process.Kill();cmd.Wait()}()
 scanner:=bufio.NewScanner(stdout)
 if !scanner.Scan() || scanner.Text()!="locked" {t.Fatal("child failed to acquire lock")}
 if unlock,err:=acquireClaimLock(root,"clm_crash");err!=ErrDownloadActive {
  if unlock!=nil {unlock()};t.Fatalf("live process lock not respected: %v",err)
 }
 if err:=cmd.Process.Kill();err!=nil {t.Fatal(err)}
 cmd.Wait()
 unlock,err:=acquireClaimLock(root,"clm_crash");if err!=nil {t.Fatalf("crashed process lock retained: %v",err)}
 if err:=unlock();err!=nil {t.Fatal(err)}
}
