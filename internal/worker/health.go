// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jkaninda/logger"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const healthProbeTimeout = 2 * time.Second

type HealthResponse struct {
	Status string `json:"status"`
}

type ReadyResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Redis    string `json:"redis"`
}

type HealthServer struct {
	db    *gorm.DB
	redis *redis.Client
	srv   *http.Server
}

func NewHealthServer(addr string, db *gorm.DB, rdb *redis.Client) *HealthServer {
	h := &HealthServer{db: db, redis: rdb}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/readyz", h.readyz)
	h.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return h
}

func (h *HealthServer) Start() func() {
	go func() {
		if err := h.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("worker health server stopped", "addr", h.srv.Addr, "error", err)
		}
	}()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.srv.Shutdown(ctx)
	}
}

func (h *HealthServer) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok"})
}

func (h *HealthServer) readyz(w http.ResponseWriter, r *http.Request) {
	resp := ReadyResponse{Status: "ready", Database: "ok", Redis: "ok"}

	if err := h.pingDB(r.Context()); err != nil {
		resp.Status, resp.Database = "not ready", err.Error()
	}
	if err := h.pingRedis(r.Context()); err != nil {
		resp.Status, resp.Redis = "not ready", err.Error()
	}

	code := http.StatusOK
	if resp.Status != "ready" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, resp)
}

func (h *HealthServer) pingDB(ctx context.Context) error {
	if h.db == nil {
		return errors.New("not configured")
	}
	sqlDB, err := h.db.DB()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	return sqlDB.PingContext(ctx)
}

func (h *HealthServer) pingRedis(ctx context.Context) error {
	if h.redis == nil {
		return errors.New("not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	return h.redis.Ping(ctx).Err()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
