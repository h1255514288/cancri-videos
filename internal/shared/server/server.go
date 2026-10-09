package server

import (
 "context"
 "encoding/json"
 "errors"
 "log/slog"
 "net"
 "net/http"
 "time"

 "github.com/yourorg/video-distribution-go/internal/shared/config"
)

type Readiness func(context.Context) error

func Handler(role string, ready Readiness) http.Handler {
 return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  w.Header().Set("Cache-Control", "no-store")
  w.Header().Set("X-Content-Type-Options", "nosniff")
  if r.URL.Path != "/health/live" && r.URL.Path != "/health/ready" {
   http.NotFound(w, r); return
  }
  if r.Method != http.MethodGet && r.Method != http.MethodHead {
   w.Header().Set("Allow", "GET, HEAD")
   w.WriteHeader(http.StatusMethodNotAllowed); return
  }
  status, code := "alive", http.StatusOK
  if r.URL.Path == "/health/ready" {
   status = "ready"
   ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
   defer cancel()
   if ready == nil || ready(ctx) != nil { status, code = "not_ready", http.StatusServiceUnavailable }
  }
  w.Header().Set("Content-Type", "application/json")
  w.WriteHeader(code)
  if r.Method != http.MethodHead { _ = json.NewEncoder(w).Encode(map[string]string{"service":role, "status":status}) }
 })
}

func Run(ctx context.Context, cfg config.Config, handler http.Handler, logger *slog.Logger) error {
 listener, err := net.Listen("tcp", cfg.Address)
 if err != nil { return err }
 return Serve(ctx, listener, cfg, handler, logger)
}

func Serve(ctx context.Context, listener net.Listener, cfg config.Config, handler http.Handler, logger *slog.Logger) error {
 srv := &http.Server{Handler:handler, ReadHeaderTimeout:5*time.Second, IdleTimeout:60*time.Second, MaxHeaderBytes:16*1024}
 result := make(chan error, 1)
 go func(){ result <- srv.Serve(listener) }()
 logger.Info("service started", "service",cfg.Role, "address",listener.Addr().String())
 select {
 case err := <-result:
  if errors.Is(err, http.ErrServerClosed) { return nil }; return err
 case <-ctx.Done():
  shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
  defer cancel()
  if err := srv.Shutdown(shutdownCtx); err != nil { _ = srv.Close(); <-result; return err }
  err := <-result
  if errors.Is(err, http.ErrServerClosed) { return nil }; return err
 }
}
