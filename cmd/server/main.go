// Command server runs the food-ordering API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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

	start := time.Now()
	codes, err := promo.LoadIndexFile(indexPath)
	if err != nil {
		return err
	}
	validator := promo.NewSetValidator(codes)
	log.Info("coupon index loaded", "path", indexPath, "codes", validator.Len(), "took", time.Since(start))

	products, err := catalog.NewSeeded()
	if err != nil {
		return err
	}

	srv := &httpapi.Server{
		Catalog: products,
		Orders:  &order.Service{Catalog: products, Promo: validator, Repo: order.NewMemoryRepository()},
		APIKey:  apiKey,
		Log:     log,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGHUP reloads the coupon index without a restart; readers never block.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			codes, err := promo.LoadIndexFile(indexPath)
			if err != nil {
				log.Error("coupon index reload failed; keeping old index", "err", err)
				continue
			}
			validator.Replace(codes)
			log.Info("coupon index reloaded", "codes", validator.Len())
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

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
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
