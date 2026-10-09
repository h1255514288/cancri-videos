package admin

import (
 "net/http/httptest"
 "strings"
 "testing"
)

func TestManagementResponsesDisableCaching(t *testing.T) {
 handler:=NewHandler(NewService(nil,testSecret),nil)
 for _,path:=range []string{"/admin/login","/admin/api_keys","/admin/users","/admin/unknown"} {
  recorder:=httptest.NewRecorder()
  handler.ServeHTTP(recorder,httptest.NewRequest("POST",path,strings.NewReader(`null`)))
  for key,want:=range map[string]string{"Cache-Control":"no-store","X-Content-Type-Options":"nosniff","Referrer-Policy":"no-referrer"} {
   if got:=recorder.Header().Get(key);got!=want {t.Errorf("%s %s=%q, want %q",path,key,got,want)}
  }
 }
}
