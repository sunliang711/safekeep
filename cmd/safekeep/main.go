package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/config"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/migration"
	"safekeep/internal/queue"
	"safekeep/internal/scheduler"
	"safekeep/internal/server"
	"safekeep/internal/storage"
	"safekeep/internal/storage/dropbox"
	"safekeep/internal/storage/s3"
	"safekeep/web"
)

func main() {
	var boot config.BootstrapConfig
	flag.StringVar(&boot.ListenAddr, "listen", envOr("SAFEKEEP_LISTEN", ":8080"), "HTTP listen address")
	flag.StringVar(&boot.DataDir, "data-dir", envOr("SAFEKEEP_DATA_DIR", "./data"), "Data directory for SQLite, master key, and spool")
	flag.StringVar(&boot.MasterKeyHex, "master-key", os.Getenv("SAFEKEEP_MASTER_KEY"), "Optional 32-byte master key (64 hex chars or base64)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	masterKey, err := config.EnsureMasterKey(boot.DataDir, boot.MasterKeyHex)
	if err != nil {
		logger.Error("init master key failed", "err", err)
		os.Exit(1)
	}
	cipher, err := crypto.NewCipher(masterKey)
	if err != nil {
		logger.Error("init cipher failed", "err", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(boot.DataDir, "safekeep.db")
	store, err := db.Open(dbPath, boot.DataDir)
	if err != nil {
		logger.Error("open database failed", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	settings, err := store.GetSystemSettings(context.Background())
	if err != nil {
		logger.Error("load system settings failed", "err", err)
		os.Exit(1)
	}
	_ = os.MkdirAll(settings.SpoolDir, 0o750)

	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := storage.NewRegistry(store, cipher)
	registry.RegisterBuilder("s3", s3.Builder)
	registry.RegisterBuilder("dropbox", dropbox.Builder)

	auditLog := audit.NewLogger(store)
	backupSvc := backup.NewService(appCtx, store, cipher, registry)
	pool := queue.NewWorkerPool(store, backupSvc, settings.WorkerConcurrency, logger)
	backupSvc.SetEnqueuer(pool)
	pool.Start(appCtx)

	sched := scheduler.NewEngine(appCtx, store, registry, auditLog, logger)
	if err := sched.Start(); err != nil {
		logger.Error("start cron scheduler failed", "err", err)
		os.Exit(1)
	}

	migSvc := migration.NewService(store, cipher)
	webFS, err := web.DistFS()
	if err != nil {
		logger.Error("load embedded web assets failed", "err", err)
		os.Exit(1)
	}

	srv := server.New(store, cipher, registry, backupSvc, pool, sched, migSvc, auditLog, logger, webFS)
	httpSrv := &http.Server{
		Addr:              boot.ListenAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		logger.Info("SafeKeep HTTP server listening", "addr", boot.ListenAddr, "data_dir", boot.DataDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting down gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	sched.Stop()
	cancel()
	pool.Wait()
	logger.Info("SafeKeep stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
