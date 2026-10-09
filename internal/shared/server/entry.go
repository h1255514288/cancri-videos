package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/yourorg/video-distribution-go/internal/control/admin"
	"github.com/yourorg/video-distribution-go/internal/control/api"
	"github.com/yourorg/video-distribution-go/internal/node/download"
	"github.com/yourorg/video-distribution-go/internal/shared/config"
	"github.com/yourorg/video-distribution-go/internal/shared/db"
	"github.com/yourorg/video-distribution-go/internal/shared/tasks"
)

func Main(role string) int {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(role, os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		return 1
	}

	dbCfg := db.Config{
		Host:     cfg.DBHost,
		Port:     cfg.DBPort,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		DBName:   cfg.DBName,
		SSLMode:  cfg.DBSSLMode,
		MaxConns: cfg.DBMaxConns,
		MaxIdle:  cfg.DBMaxConns / 2,
	}
	database, err := db.Connect(context.Background(), dbCfg)
	if err != nil {
		logger.Error("database connect failed", "error", err)
		return 1
	}
	defer database.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cleanup := tasks.NewCleanupService(database, logger)
	var configuredNodeID int64
	if role == "node" {
		nodeID, err := strconv.ParseInt(os.Getenv("VDS_NODE_ID"), 10, 64)
		if err != nil || nodeID <= 0 {
			logger.Error("VDS_NODE_ID must be a positive integer")
			return 1
		}
		var root string
		if err := database.GetContext(ctx, &root, `SELECT storage_root_path FROM storage_nodes WHERE id=$1 AND status IN ('active','readonly')`, nodeID); err != nil {
			logger.Error("node configuration unavailable", "error", err)
			return 1
		}
		configuredNodeID = nodeID
		cleanup = tasks.NewNodeCleanupService(database, logger, nodeID, root)
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); cleanup.RunPeriodic(workerCtx, time.Minute) }()
	defer func() { cancelWorker(); <-workerDone }()
	handler := ApplicationHandler(role, database, cfg.JWTSecret, logger, configuredNodeID)
	if err := Run(ctx, cfg, handler, logger); err != nil {
		logger.Error("service stopped with error", "error", err)
		return 1
	}
	return 0
}

func ApplicationHandler(role string, database *sqlx.DB, jwtSecret string, logger *slog.Logger, nodeIDs ...int64) http.Handler {
	mux := http.NewServeMux()
	health := Handler(role, database.PingContext)
	mux.Handle("/health/live", health)
	mux.Handle("/health/ready", health)
	if role == "control" {
		adminService := admin.NewService(database, jwtSecret)
		mux.Handle("/admin/", admin.NewHandler(adminService, logger))
		mux.Handle("/", api.NewHandler(database, jwtSecret, logger))
	} else {
		mux.Handle("/d/", download.NewHandler(database, jwtSecret, logger, nodeIDs...))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		})
	}
	return mux
}
