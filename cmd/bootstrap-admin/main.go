package main

import (
 "context"
 "fmt"
 "os"
 "time"

 "github.com/yourorg/video-distribution-go/internal/shared/config"
 "github.com/yourorg/video-distribution-go/internal/shared/db"
 "golang.org/x/crypto/bcrypt"
)

func main() {
 if err:=run();err!=nil {fmt.Fprintln(os.Stderr,err);os.Exit(1)}
}

func run() error {
 name,password:=os.Getenv("VDS_ADMIN_USERNAME"),os.Getenv("VDS_ADMIN_PASSWORD")
 if name=="" || len(password)<12 || len(password)>72 {return fmt.Errorf("set VDS_ADMIN_USERNAME and a 12–72 byte VDS_ADMIN_PASSWORD")}
 cfg,err:=config.Load("control",os.Getenv);if err!=nil{return err}
 ctx,cancel:=context.WithTimeout(context.Background(),15*time.Second);defer cancel()
 database,err:=db.Connect(ctx,db.Config{Host:cfg.DBHost,Port:cfg.DBPort,User:cfg.DBUser,Password:cfg.DBPassword,DBName:cfg.DBName,SSLMode:cfg.DBSSLMode,MaxConns:2,MaxIdle:1});if err!=nil{return err};defer database.Close()
 hash,err:=bcrypt.GenerateFromPassword([]byte(password),bcrypt.DefaultCost);if err!=nil{return err}
 result,err:=database.ExecContext(ctx,`INSERT INTO admins (username,password_hash) VALUES ($1,$2) ON CONFLICT (username) DO NOTHING`,name,string(hash));if err!=nil{return err}
 n,err:=result.RowsAffected();if err!=nil{return err};if n==0{return fmt.Errorf("administrator already exists; no password changed")}
 fmt.Println("administrator created")
 return nil
}
