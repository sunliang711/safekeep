package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

var version = "dev"

func main() {
	var configFlag, listenFlag, dataDirFlag, masterKeyFlag string
	var showVersion bool
	flag.StringVar(&configFlag, "config", "", "Path to config.toml (or set SAFEKEEP_CONFIG)")
	flag.StringVar(&listenFlag, "listen", config.DefaultListenAddr, "HTTP listen address (overrides SAFEKEEP_LISTEN and config.toml)")
	flag.StringVar(&dataDirFlag, "data-dir", "./data", "Data directory for SQLite, master key, and spool")
	flag.StringVar(&masterKeyFlag, "master-key", "", "Optional 32-byte master key (64 hex chars or base64)")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return
	}

	cliOverrides := map[string]string{}
	flag.Visit(func(f *flag.Flag) {
		cliOverrides[f.Name] = f.Value.String()
	})

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	boot, err := config.Load(configFlag, cliOverrides)
	if err != nil {
		logger.Error("load config failed", "err", err)
		os.Exit(1)
	}

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
	store, err := db.OpenWithDefaults(dbPath, boot.DataDir, boot.Defaults)
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
	if overridden, changed := config.ApplyEnvOverrides(settings); changed {
		if err := store.UpdateSystemSettings(context.Background(), overridden, "env"); err != nil {
			logger.Error("apply environment overrides failed", "err", err)
			os.Exit(1)
		}
		settings = overridden
	}
	if boot.HasSMTPSection {
		existingSMTP, err := store.GetSMTPConfig(context.Background(), cipher)
		if err == nil {
			_, envSMTP := config.ApplySMTPEnvOverrides(existingSMTP)
			if envSMTP {
				merged, _ := config.ApplySMTPEnvOverrides(existingSMTP)
				_ = store.SaveSMTPConfig(context.Background(), cipher, merged, "env")
			} else if !existingSMTP.Enabled && existingSMTP.Host == "" && boot.SMTPDefaults.Host != "" {
				_ = store.SaveSMTPConfig(context.Background(), cipher, boot.SMTPDefaults, "config")
			}
		}
	}
	_ = os.MkdirAll(settings.SpoolDir, 0o750)

	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registry := storage.NewRegistry(store, cipher)
	registry.RegisterBuilder("s3", s3.Builder)
	registry.RegisterBuilder("dropbox", dropbox.Builder)

	auditLog := audit.NewLogger(store)
	backupSvc := backup.NewService(appCtx, store, cipher, registry)
	backupSvc.SetAuditLogger(auditLog)
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
	srv.SetCORSOrigins(boot.CORSAllowedOrigins)
	httpSrv := &http.Server{
		Addr:              boot.ListenAddr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		logger.Info("SafeKeep HTTP server listening", "version", version, "addr", boot.ListenAddr, "data_dir", boot.DataDir, "config", boot.ConfigPath)
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
	backupSvc.Wait()
	logger.Info("SafeKeep stopped")
}
