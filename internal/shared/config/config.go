package config

import (
 "fmt"
 "net"
 "strconv"
 "time"
)

type Config struct {
 Role            string
 Address         string
 ShutdownTimeout time.Duration
 DBHost          string
 DBPort          int
 DBUser          string
 DBPassword      string
 DBName          string
 DBSSLMode       string
 DBMaxConns      int
 JWTSecret       string
}

func Load(role string, getenv func(string) string) (Config, error) {
 c := Config{Role: role, ShutdownTimeout: 15*time.Second}
 switch role {
 case "control": c.Address = "127.0.0.1:8080"
 case "node": c.Address = "127.0.0.1:8001"
 default: return Config{}, fmt.Errorf("unknown service role %q", role)
 }
 if v := getenv("VDS_LISTEN_ADDR"); v != "" { c.Address = v }
 _, port, err := net.SplitHostPort(c.Address)
 if err != nil { return Config{}, fmt.Errorf("invalid listen address: %w", err) }
 n, err := strconv.Atoi(port)
 if err != nil || n < 1 || n > 65535 { return Config{}, fmt.Errorf("invalid listen port") }
 if v := getenv("VDS_SHUTDOWN_TIMEOUT"); v != "" {
  d, err := time.ParseDuration(v)
  if err != nil || d <= 0 || d > 10*time.Minute { return Config{}, fmt.Errorf("shutdown timeout must be in (0, 10m]") }
  c.ShutdownTimeout = d
 }
 c.DBHost = getenv("VDS_DB_HOST")
 if c.DBHost == "" { c.DBHost = "localhost" }
 c.DBPort = 5432
 if v := getenv("VDS_DB_PORT"); v != "" {
  p, err := strconv.Atoi(v)
  if err != nil || p <= 0 || p >= 65536 { return Config{}, fmt.Errorf("invalid database port") }
  c.DBPort = p
 }
 c.DBUser = getenv("VDS_DB_USER")
 if c.DBUser == "" { c.DBUser = "postgres" }
 c.DBPassword = getenv("VDS_DB_PASSWORD")
 c.DBName = getenv("VDS_DB_NAME")
 if c.DBName == "" { c.DBName = "video_distribution" }
 c.DBSSLMode = getenv("VDS_DB_SSLMODE")
 if c.DBSSLMode == "" { c.DBSSLMode = "disable" }
 c.DBMaxConns = 50
 if v := getenv("VDS_DB_MAX_CONNS"); v != "" {
  n, err := strconv.Atoi(v)
  if err != nil || n <= 0 { return Config{}, fmt.Errorf("invalid database pool size") }
  c.DBMaxConns = n
 }
 c.JWTSecret = getenv("VDS_JWT_SECRET")
 if len(c.JWTSecret) < 32 { return Config{}, fmt.Errorf("VDS_JWT_SECRET must contain at least 32 bytes") }
 return c, nil
}
