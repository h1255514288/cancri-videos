package db

import (
 "context"
 "database/sql"
 "fmt"
 "net"
 "net/url"
 "strconv"
 "time"

 "github.com/jmoiron/sqlx"
 _ "github.com/lib/pq"
)

type Config struct {
 Host     string
 Port     int
 User     string
 Password string
 DBName   string
 SSLMode  string
 MaxConns int
 MaxIdle  int
}

func Connect(ctx context.Context, cfg Config) (*sqlx.DB, error) {
 connection := url.URL{Scheme:"postgres", Host:net.JoinHostPort(cfg.Host,strconv.Itoa(cfg.Port)), User:url.UserPassword(cfg.User,cfg.Password), Path:"/"+cfg.DBName}
 query:=url.Values{}
 query.Set("sslmode",cfg.SSLMode)
 connection.RawQuery=query.Encode()
 dsn := connection.String()
 
 db, err := sqlx.ConnectContext(ctx, "postgres", dsn)
 if err != nil {
  return nil, fmt.Errorf("connect: %w", err)
 }
 
 db.SetMaxOpenConns(cfg.MaxConns)
 db.SetMaxIdleConns(cfg.MaxIdle)
 db.SetConnMaxLifetime(time.Hour)
 
 if err := db.PingContext(ctx); err != nil {
  db.Close()
  return nil, fmt.Errorf("ping: %w", err)
 }
 
 return db, nil
}

type Tx interface {
 sqlx.QueryerContext
 sqlx.ExecerContext
 GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
 SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
 Commit() error
 Rollback() error
}

func Begin(ctx context.Context, db *sqlx.DB, opts *sql.TxOptions) (Tx, error) {
 return db.BeginTxx(ctx, opts)
}
