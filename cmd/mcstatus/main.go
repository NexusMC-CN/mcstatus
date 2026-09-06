package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/blockbridge/avmcbbs/apps/mcstatus/config"
	"github.com/blockbridge/avmcbbs/apps/mcstatus/internal/api"
	"github.com/blockbridge/avmcbbs/apps/mcstatus/internal/probe"
)

func main() {
	cfg := config.Load()
	service := probe.NewService(cfg.Workers, cfg.QueueSize, cfg.Timeout, cfg.JobTTL)
	service.Start()
	defer service.Close()

	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           api.NewRouter(service),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("mcstatus listening on %s", cfg.ListenAddress)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("mcstatus server exited: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("mcstatus shutdown error: %v", err)
	}
}
