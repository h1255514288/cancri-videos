package download

import (
 "crypto/sha256"
 "fmt"
 "os"
 "path/filepath"

 "github.com/gofrs/flock"
)

func acquireClaimLock(root, claimID string) (func() error, error) {
 dir := filepath.Join(root, ".download-locks")
 if err := os.MkdirAll(dir, 0700); err != nil { return nil, err }
 info, err := os.Lstat(dir)
 if err != nil { return nil, err }
 if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 { return nil, fmt.Errorf("invalid download lock directory") }
 key := sha256.Sum256([]byte(claimID))
 lock := flock.New(filepath.Join(dir, fmt.Sprintf("%x.lock", key)))
 acquired, err := lock.TryLock()
 if err != nil { return nil, err }
 if !acquired { return nil, ErrDownloadActive }
 return lock.Close, nil
}
