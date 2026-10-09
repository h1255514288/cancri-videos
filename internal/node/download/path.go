package download

import (
 "errors"
 "path/filepath"
 "strings"
)

func safeFilePath(root, name string) (string,error) {
 if root=="" || !filepath.IsLocal(name) {return "",errors.New("invalid storage path")}
 resolvedRoot,err:=filepath.EvalSymlinks(root);if err!=nil{return "",err}
 resolvedRoot,err=filepath.Abs(resolvedRoot);if err!=nil{return "",err}
 path,err:=filepath.EvalSymlinks(filepath.Join(resolvedRoot,name));if err!=nil{return "",err}
 path,err=filepath.Abs(path);if err!=nil{return "",err}
 rel,err:=filepath.Rel(resolvedRoot,path)
 if err!=nil || rel==".." || strings.HasPrefix(rel,".."+string(filepath.Separator)) || filepath.IsAbs(rel) {return "",errors.New("storage path escapes root")}
 return path,nil
}
