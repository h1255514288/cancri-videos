//go:build integration

package integration

import (
 "bytes"
 "encoding/json"
 "io"
 "mime/multipart"
 "net/http"
 "net/http/httptest"
 "testing"

 "github.com/yourorg/video-distribution-go/internal/control/admin"
 "golang.org/x/crypto/bcrypt"
)

func TestAdminLoginAndUpload(t *testing.T) {
 db:=fixture(t,0,10,3);root:=t.TempDir()
 exec(t,db,`UPDATE storage_nodes SET storage_root_path=$1 WHERE id=2`,root)
 hash,err:=bcrypt.GenerateFromPassword([]byte("integration-admin-password"),bcrypt.MinCost);if err!=nil {t.Fatal(err)}
 exec(t,db,`INSERT INTO admins (username,password_hash) VALUES ('operator',$1)`,string(hash))
 srv:=httptest.NewServer(admin.NewHandler(admin.NewService(db,"integration-admin-secret-32-bytes-minimum"),nil));defer srv.Close()
 resp,err:=http.Post(srv.URL+"/admin/login","application/json",bytes.NewBufferString(`{"username":"operator","password":"integration-admin-password"}`));if err!=nil {t.Fatal(err)}
 var login admin.LoginResponse
 err=json.NewDecoder(resp.Body).Decode(&login);resp.Body.Close();if err!=nil || resp.StatusCode!=200 || login.Token=="" {t.Fatalf("login %d %v",resp.StatusCode,err)}
 var body bytes.Buffer
 writer:=multipart.NewWriter(&body)
 writer.WriteField("category_id","3");writer.WriteField("node_id","2")
 part,err:=writer.CreateFormFile("file","test.mp4");if err!=nil {t.Fatal(err)}
 part.Write([]byte("uploaded video bytes"));writer.Close()
 req,err:=http.NewRequest("POST",srv.URL+"/admin/videos",&body);if err!=nil {t.Fatal(err)}
 req.Header.Set("Authorization","Bearer "+login.Token);req.Header.Set("Content-Type",writer.FormDataContentType())
 resp,err=http.DefaultClient.Do(req);if err!=nil {t.Fatal(err)}
 data,_:=io.ReadAll(resp.Body);resp.Body.Close()
 if resp.StatusCode!=201 {t.Fatalf("upload: %d %s",resp.StatusCode,data)}
 if scalar(t,db,`SELECT COUNT(*) FROM videos WHERE status='available'`)!=1 {t.Fatal("missing uploaded inventory")}
}
