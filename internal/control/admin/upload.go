package admin

import (
 "crypto/rand"
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "strconv"
 "strings"
)

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
 r.Body=http.MaxBytesReader(w,r.Body,1<<30)
 if err:=r.ParseMultipartForm(8<<20);err!=nil {http.Error(w,"invalid or oversized upload",http.StatusBadRequest);return}
 defer r.MultipartForm.RemoveAll()
 categoryID,e1:=strconv.ParseInt(r.FormValue("category_id"),10,64)
 nodeID,e2:=strconv.ParseInt(r.FormValue("node_id"),10,64)
 if e1!=nil || e2!=nil || categoryID<=0 || nodeID<=0 {http.Error(w,"invalid category or node",400);return}
 var root string
 err:=h.service.db.GetContext(r.Context(),&root,`SELECT n.storage_root_path FROM storage_nodes n WHERE n.id=$1 AND n.status='active' AND EXISTS (SELECT 1 FROM categories WHERE id=$2 AND status='enabled')`,nodeID,categoryID)
 if err!=nil {http.Error(w,"category or node unavailable",400);return}
 source,header,err:=r.FormFile("file");if err!=nil {http.Error(w,"missing file",400);return};defer source.Close()
 filename:=filepath.Base(strings.ReplaceAll(header.Filename,"\\","/"))
 if filename=="." || len(filename)>255 {http.Error(w,"invalid filename",400);return}
 var idBytes [15]byte
 if _,err:=rand.Read(idBytes[:]);err!=nil {http.Error(w,"internal_error",500);return}
 id:="vid_"+hex.EncodeToString(idBytes[:])[:24]
 if err:=os.MkdirAll(root,0750);err!=nil {http.Error(w,"storage unavailable",500);return}
 destination:=filepath.Join(root,id)
 file,err:=os.OpenFile(destination,os.O_CREATE|os.O_EXCL|os.O_WRONLY,0600)
 if err!=nil {http.Error(w,"storage unavailable",500);return}
 committed:=false
 defer func(){file.Close();if !committed {os.Remove(destination)}}()
 hash:=sha256.New()
 size,err:=io.Copy(io.MultiWriter(file,hash),source)
 if err!=nil || size==0 {http.Error(w,"invalid file",400);return}
 if err:=file.Sync();err!=nil {http.Error(w,"storage error",500);return}
 if err:=file.Close();err!=nil {http.Error(w,"storage error",500);return}
 digest:=hex.EncodeToString(hash.Sum(nil))
 mimeType:=header.Header.Get("Content-Type");if len(mimeType)>64 || mimeType=="" {mimeType="application/octet-stream"}
 adminID:=r.Context().Value(adminContextKey{}).(int64)
 _,err=h.service.db.ExecContext(r.Context(),`INSERT INTO videos (video_id,category_id,node_id,title,filename,storage_path,size_bytes,sha256,mime_type,status,uploaded_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'available',$10)`,id,categoryID,nodeID,filename,filename,id,size,digest,mimeType,adminID)
 if err!=nil {h.logger.Error("upload metadata failed","error",err);http.Error(w,"internal_error",500);return}
 committed=true
 w.Header().Set("Content-Type","application/json");w.WriteHeader(http.StatusCreated)
 json.NewEncoder(w).Encode(map[string]interface{}{"video_id":id,"size_bytes":size,"sha256":digest,"status":"available","filename":filename,"download_node":fmt.Sprint(nodeID)})
}
