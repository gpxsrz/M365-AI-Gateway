package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"m365-native/internal/outbound"
	"m365-native/internal/web"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const requestReadTimeout = 30 * time.Minute
const shutdownTimeout = 30 * time.Second

func sidecarHTTPServer(listen string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       requestReadTimeout,
		IdleTimeout:       120 * time.Second,
		WriteTimeout:      0, // streaming endpoints need an open-ended write window.
	}
}

func serveUntilSignal(ctx context.Context, listen func() error, shutdown func(context.Context) error, closeRuntime func() error) (err error) {
	defer func() { err = errors.Join(err, closeRuntime()) }()
	shutdownDone := make(chan error, 1)
	stopShutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			shutdownDone <- shutdown(shutdownContext)
		case <-stopShutdown:
			shutdownDone <- nil
		}
	}()

	listenErr := listen()
	close(stopShutdown)
	shutdownErr := <-shutdownDone
	if errors.Is(listenErr, http.ErrServerClosed) {
		listenErr = nil
	}
	return errors.Join(listenErr, shutdownErr)
}

func run() error {
	if err := web.ApplyStartupSettingsEnv(); err != nil {
		return fmt.Errorf("load runtime settings: %w", err)
	}
	if err := outbound.ConfigureFromEnv(); err != nil {
		return fmt.Errorf("configure outbound proxy: %w", err)
	}
	s, err := web.New()
	if err != nil {
		return err
	}
	listen := "127.0.0.1:4141"
	if v := os.Getenv("M365_LISTEN"); v != "" {
		listen = v
	}
	log.Printf("m365-native listening on http://%s\\n", listen)
	server := sidecarHTTPServer(listen, s.Routes())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveUntilSignal(ctx, server.ListenAndServe, func(ctx context.Context) error {
		if err := server.Shutdown(ctx); err != nil {
			return errors.Join(err, server.Close())
		}
		return nil
	}, s.Close)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
