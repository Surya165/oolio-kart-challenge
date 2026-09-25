// Command server runs the food-ordering API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"kart/internal/catalog"
	"kart/internal/httpapi"
	"kart/internal/order"
	"kart/internal/promo"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	addr := env("ADDR", ":8080")
	indexPath := env("COUPON_INDEX", "data/valid_coupons.txt")
	apiKey := env("API_KEY", "apitest")
	// How long to keep serving after SIGTERM while /readyz reports 503, so a
	// load balancer can stop routing here before connections are closed.
	drain, err := time.ParseDuration(env("SHUTDOWN_DRAIN", "0s"))
	if err != nil {
		return fmt.Errorf("SHUTDOWN_DRAIN: %w", err)
	}

	// Where the index lives is behind promo.IndexSource; a file today.
	index := promo.FileIndex{Path: indexPath}
	validator := promo.NewSetValidator(nil)
	start := time.Now()
	if _, err := validator.LoadFrom(context.Background(), index); err != nil {
		return err
	}
	log.Info("coupon index loaded", "source", index.String(), "codes", validator.Len(), "took", time.Since(start))

	products, err := catalog.NewSeeded()
	if err != nil {
		return err
	}

	var ready atomic.Bool
	srv := &httpapi.Server{
		Catalog: products,
		Orders:  &order.Service{Catalog: products, Promo: validator, Repo: order.NewMemoryRepository()},
		APIKey:  apiKey,
		Log:     log,
		Ready:   ready.Load,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGHUP reloads the coupon index without a restart; readers never block.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			n, err := validator.LoadFrom(context.Background(), index)
			if err != nil {
				log.Error("coupon index reload failed; keeping old index", "err", err)
				continue
			}
			log.Info("coupon index reloaded", "codes", n)
		}
	}()

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Serve(ln) }()
	ready.Store(true) // index and catalog are loaded and the port is open
	log.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		ready.Store(false)
		log.Info("shutting down", "drain", drain.String())
		time.Sleep(drain)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
