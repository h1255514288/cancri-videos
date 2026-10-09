package server

import (
 "context"
 "errors"
 "io"
 "log/slog"
 "net"
 "net/http"
 "net/http/httptest"
 "testing"
 "time"

 "github.com/yourorg/video-distribution-go/internal/shared/config"
)

func TestHealth(t *testing.T) {
 for _, tt := range []struct{name,path,method string; ready Readiness; want int}{
  {"live","/health/live","GET",nil,200},
  {"unconfigured","/health/ready","GET",nil,503},
  {"ready","/health/ready","GET",func(context.Context)error{return nil},200},
  {"dependency failure","/health/ready","GET",func(context.Context)error{return errors.New("secret")},503},
  {"method","/health/live","POST",nil,405},
  {"unknown","/api/v1/claims","GET",nil,404},
 } {
  t.Run(tt.name,func(t *testing.T){
   w:=httptest.NewRecorder()
   Handler("control",tt.ready).ServeHTTP(w,httptest.NewRequest(tt.method,tt.path,nil))
   if w.Code!=tt.want {t.Fatalf("got %d want %d",w.Code,tt.want)}
   if w.Header().Get("Cache-Control")!="no-store" {t.Fatal("health response cacheable")}
  })
 }
}

func TestServeAndShutdown(t *testing.T){
 l,err:=net.Listen("tcp","127.0.0.1:0"); if err!=nil{t.Fatal(err)}
 ctx,cancel:=context.WithCancel(context.Background()); defer cancel()
 result:=make(chan error,1)
 go func(){result<-Serve(ctx,l,config.Config{Role:"node",ShutdownTimeout:time.Second},Handler("node",nil),slog.New(slog.NewTextHandler(io.Discard,nil)))}()
 client:=&http.Client{Timeout:2*time.Second}
 defer client.CloseIdleConnections()
 resp,err:=client.Get("http://"+l.Addr().String()+"/health/live")
 if err!=nil{t.Fatal(err)}
 resp.Body.Close()
 if resp.StatusCode!=200{t.Fatal(resp.StatusCode)}
 cancel()
 select {case err:=<-result: if err!=nil{t.Fatal(err)}; case <-time.After(3*time.Second):t.Fatal("shutdown stuck")}
}
