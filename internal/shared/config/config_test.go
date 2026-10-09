package config

import "testing"

func TestLoad(t *testing.T){
 for _,role:=range []string{"control","node"}{
  c,err:=Load(role,func(k string)string{if k=="VDS_JWT_SECRET" {return "test-secret-at-least-32-bytes-long"};return ""})
  if err!=nil||c.Address==""{t.Fatalf("%s: %+v %v",role,c,err)}
 }
 for _,env:=range []map[string]string{
  {"VDS_JWT_SECRET":"short"},
  {"VDS_DB_PORT":"bad"},
  {"VDS_DB_MAX_CONNS":"-1"},
  {"VDS_LISTEN_ADDR":"bad"},
  {"VDS_LISTEN_ADDR":"localhost:0"},
  {"VDS_LISTEN_ADDR":"localhost:65536"},
  {"VDS_SHUTDOWN_TIMEOUT":"0s"},
  {"VDS_SHUTDOWN_TIMEOUT":"bad"},
  {"VDS_SHUTDOWN_TIMEOUT":"11m"},
 }{
  if _,err:=Load("control",func(k string)string{if v,ok:=env[k];ok{return v};if k=="VDS_JWT_SECRET" {return "test-secret-at-least-32-bytes-long"};return ""});err==nil{t.Fatalf("accepted invalid config: %v",env)}
 }
 if _,err:=Load("unknown",func(string)string{return ""});err==nil{t.Fatal("accepted unknown role")}
}
