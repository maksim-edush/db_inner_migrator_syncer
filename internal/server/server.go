package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"db_inner_migrator_syncer/internal/config"
	"db_inner_migrator_syncer/internal/db"
	"db_inner_migrator_syncer/internal/diff"
	"db_inner_migrator_syncer/internal/migrate"
	"db_inner_migrator_syncer/internal/storage"
)

// Server exposes an HTTP UI + JSON API for the migrator.
type Server struct {
	cfg *config.Config
	mux *http.ServeMux
}

// New constructs a Server for a loaded configuration.
func New(cfg *config.Config) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}
	if err := storage.EnsureBase(cfg.StoragePath()); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s, nil
}

// Handler returns the server mux to pass to http.ListenAndServe.
func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.Handle("/static/", s.staticFiles())
	s.mux.HandleFunc("/", s.serveIndex)
	s.mux.HandleFunc("/api/pairs", s.handlePairs)
	s.mux.HandleFunc("/api/diff", s.handleDiff)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/pending", s.handlePending)
	s.mux.HandleFunc("/api/scripts", s.handleScripts)
	s.mux.HandleFunc("/api/script", s.handleScriptContent)
	s.mux.HandleFunc("/api/apply", s.handleApply)
	s.mux.HandleFunc("/api/rollback", s.handleRollback)
}

func (s *Server) handlePairs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type pairDTO struct {
		Name               string `json:"name"`
		StagingProvider    string `json:"staging_provider"`
		ProductionProvider string `json:"production_provider"`
	}
	out := make([]pairDTO, 0, len(s.cfg.Pairs))
	for _, p := range s.cfg.Pairs {
		out = append(out, pairDTO{
			Name:               p.Name,
			StagingProvider:    p.Staging.Provider,
			ProductionProvider: p.Production.Provider,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pair, err := s.pairFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	stagingAdapter, err := db.Open(pair.Staging)
	if err != nil {
		http.Error(w, fmt.Sprintf("staging connect: %v", err), http.StatusBadRequest)
		return
	}
	defer stagingAdapter.Close()

	prodAdapter, err := db.Open(pair.Production)
	if err != nil {
		http.Error(w, fmt.Sprintf("production connect: %v", err), http.StatusBadRequest)
		return
	}
	defer prodAdapter.Close()

	stSchema, prSchema := pair.Staging.Schema, pair.Production.Schema

	stagingSchemaMeta, err := stagingAdapter.FetchSchema(ctx, stSchema)
	if err != nil {
		http.Error(w, fmt.Sprintf("staging schema: %v", err), http.StatusInternalServerError)
		return
	}
	prodSchemaMeta, err := prodAdapter.FetchSchema(ctx, prSchema)
	if err != nil {
		http.Error(w, fmt.Sprintf("production schema: %v", err), http.StatusInternalServerError)
		return
	}

	d := diff.Compare(stagingSchemaMeta, prodSchemaMeta)
	resp := struct {
		Pair    string          `json:"pair"`
		Summary string          `json:"summary"`
		Diff    diff.SchemaDiff `json:"diff"`
	}{
		Pair:    pair.Name,
		Summary: diff.Describe(d),
		Diff:    d,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pair, err := s.pairFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	stagingRows, err := s.fetchStatus(ctx, pair.Staging, pair.MigrationTable)
	if err != nil {
		http.Error(w, fmt.Sprintf("staging: %v", err), http.StatusInternalServerError)
		return
	}
	prodRows, err := s.fetchStatus(ctx, pair.Production, pair.MigrationTable)
	if err != nil {
		http.Error(w, fmt.Sprintf("production: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pair":       pair.Name,
		"staging":    stagingRows,
		"production": prodRows,
	})
}

func (s *Server) handlePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pair, err := s.pairFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	scripts, err := storage.ListScripts(s.cfg.StoragePath(), pair.Name)
	if err != nil {
		http.Error(w, fmt.Sprintf("list scripts: %v", err), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	stagingStatus, err := s.latestStatusMap(ctx, pair.Staging, pair.MigrationTable)
	if err != nil {
		http.Error(w, fmt.Sprintf("staging: %v", err), http.StatusInternalServerError)
		return
	}
	prodStatus, err := s.latestStatusMap(ctx, pair.Production, pair.MigrationTable)
	if err != nil {
		http.Error(w, fmt.Sprintf("production: %v", err), http.StatusInternalServerError)
		return
	}

	type pendingItem struct {
		Name              string  `json:"name"`
		StagingStatus     string  `json:"staging_status,omitempty"`
		ProductionStatus  string  `json:"production_status,omitempty"`
		StagingError      *string `json:"staging_error,omitempty"`
		ProductionError   *string `json:"production_error,omitempty"`
		PendingStaging    bool    `json:"pending_staging"`
		PendingProduction bool    `json:"pending_production"`
	}
	out := make([]pendingItem, 0, len(scripts))
	for _, name := range scripts {
		stg := stagingStatus[name]
		prod := prodStatus[name]
		appliedStg := strings.EqualFold(stg.Status, "applied")
		appliedProd := strings.EqualFold(prod.Status, "applied")
		out = append(out, pendingItem{
			Name:              name,
			StagingStatus:     stg.Status,
			ProductionStatus:  prod.Status,
			StagingError:      stg.Error,
			ProductionError:   prod.Error,
			PendingStaging:    !appliedStg,
			PendingProduction: !appliedProd,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pair":    pair.Name,
		"entries": out,
	})
}

func (s *Server) handleScripts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		pair, err := s.pairFromRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		records, err := storage.ListScriptRecords(s.cfg.StoragePath(), pair.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("list scripts: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"pair":    pair.Name,
			"scripts": records,
		})
	case http.MethodPost:
		var req struct {
			Pair        string `json:"pair"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Forward     string `json:"forward"`
			Rollback    string `json:"rollback"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		pair, err := s.pairByName(req.Pair)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Name == "" || strings.TrimSpace(req.Forward) == "" {
			http.Error(w, "name and forward are required", http.StatusBadRequest)
			return
		}
		record, err := storage.StoreScriptContent(s.cfg.StoragePath(), pair.Name, req.Name, req.Forward, req.Rollback, req.Description)
		if err != nil {
			http.Error(w, fmt.Sprintf("store script: %v", err), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, record)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleScriptContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pair, err := s.pairFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	record, forward, rollback, err := storage.LoadScript(s.cfg.StoragePath(), pair.Name, name)
	if err != nil {
		http.Error(w, fmt.Sprintf("load script: %v", err), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pair":     pair.Name,
		"metadata": record,
		"forward":  forward,
		"rollback": rollback,
	})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Pair         string `json:"pair"`
		Name         string `json:"name"`
		Env          string `json:"env"` // optional: staging | production | ""(both)
		AutoRollback bool   `json:"autoRollback"`
		Forward      string `json:"forward"`
		Rollback     string `json:"rollback"`
		Description  string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	pair, err := s.pairByName(req.Pair)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	var forwardSQL, rollbackSQL, forwardPath, rollbackPath string
	if strings.TrimSpace(req.Forward) != "" {
		record, err := storage.StoreScriptContent(s.cfg.StoragePath(), pair.Name, req.Name, req.Forward, req.Rollback, req.Description)
		if err != nil {
			http.Error(w, fmt.Sprintf("store script: %v", err), http.StatusBadRequest)
			return
		}
		forwardPath = record.ForwardFile
		rollbackPath = record.RollbackFile
		_, forwardSQL, rollbackSQL, err = storage.LoadScript(s.cfg.StoragePath(), pair.Name, req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("load script: %v", err), http.StatusInternalServerError)
			return
		}
	} else {
		record, fwd, rb, err := storage.LoadScript(s.cfg.StoragePath(), pair.Name, req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("load script: %v", err), http.StatusBadRequest)
			return
		}
		forwardSQL = fwd
		rollbackSQL = rb
		forwardPath = record.ForwardFile
		rollbackPath = record.RollbackFile
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	runner := migrate.Runner{Pair: *pair}
	env := strings.ToLower(strings.TrimSpace(req.Env))
	switch env {
	case "", "both":
		if err := runner.Apply(ctx, req.Name, forwardSQL, rollbackSQL, forwardPath, rollbackPath, req.AutoRollback); err != nil {
			http.Error(w, fmt.Sprintf("apply failed: %v", err), http.StatusBadRequest)
			return
		}
	case "staging", "production":
		if err := runner.ApplyEnv(ctx, env, req.Name, forwardSQL, rollbackSQL, forwardPath, rollbackPath, req.AutoRollback); err != nil {
			http.Error(w, fmt.Sprintf("apply failed: %v", err), http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "env must be staging, production, or both", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "applied",
		"pair":   pair.Name,
		"name":   req.Name,
		"env":    env,
	})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Pair     string `json:"pair"`
		Name     string `json:"name"`
		Env      string `json:"env"`
		Rollback string `json:"rollback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	pair, err := s.pairByName(req.Pair)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Env == "" {
		req.Env = "staging"
	}
	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	var rollbackSQL, rollbackPath string
	if strings.TrimSpace(req.Rollback) != "" {
		rollbackSQL = req.Rollback
		rollbackPath = "inline"
	} else {
		record, _, rb, err := storage.LoadScript(s.cfg.StoragePath(), pair.Name, req.Name)
		if err != nil {
			http.Error(w, fmt.Sprintf("load script: %v", err), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(rb) == "" {
			http.Error(w, "no rollback script stored", http.StatusBadRequest)
			return
		}
		rollbackSQL = rb
		rollbackPath = record.RollbackFile
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	runner := migrate.Runner{Pair: *pair}
	if err := runner.Rollback(ctx, req.Env, req.Name, rollbackSQL, rollbackPath); err != nil {
		http.Error(w, fmt.Sprintf("rollback failed: %v", err), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "rolled_back",
		"pair":   pair.Name,
		"name":   req.Name,
		"env":    req.Env,
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	// Serve the SPA index for root and any non-API paths.
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) staticFiles() http.Handler {
	return http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
}

func (s *Server) fetchStatus(ctx context.Context, cfg config.DBConfig, table string) ([]statusEntry, error) {
	adapter, err := db.Open(cfg)
	if err != nil {
		return nil, err
	}
	defer adapter.Close()
	if err := adapter.EnsureMigrationTable(ctx, table); err != nil {
		return nil, err
	}
	rows, err := adapter.FetchStatuses(ctx, table, 25)
	if err != nil {
		return nil, err
	}
	out := make([]statusEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusEntry{
			MigrationName: r.MigrationName,
			Status:        r.Status,
			ScriptFile:    r.ScriptFile,
			RollbackFile:  r.RollbackFile,
			AppliedEnv:    r.AppliedEnv,
			Checksum:      r.Checksum,
			AppliedAt:     r.AppliedAt,
			Error:         nullToPtr(r.Error),
		})
	}
	return out, nil
}

// latestStatusMap returns a map of migration name to the latest status row per env.
func (s *Server) latestStatusMap(ctx context.Context, cfg config.DBConfig, table string) (map[string]statusEntry, error) {
	entries, err := s.fetchStatus(ctx, cfg, table)
	if err != nil {
		return nil, err
	}
	result := make(map[string]statusEntry, len(entries))
	for _, e := range entries {
		if _, exists := result[e.MigrationName]; exists {
			continue // already have latest due to ordering
		}
		result[e.MigrationName] = e
	}
	return result, nil
}

func (s *Server) pairFromRequest(r *http.Request) (*config.PairConfig, error) {
	name := r.URL.Query().Get("pair")
	return s.pairByName(name)
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) pairByName(name string) (*config.PairConfig, error) {
	if name == "" && len(s.cfg.Pairs) > 0 {
		return &s.cfg.Pairs[0], nil
	}
	pair, err := s.cfg.Pair(name)
	if err != nil {
		return nil, err
	}
	return pair, nil
}

func nullToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

type statusEntry struct {
	MigrationName string    `json:"migration_name"`
	ScriptFile    string    `json:"script_file"`
	RollbackFile  string    `json:"rollback_file,omitempty"`
	Status        string    `json:"status"`
	AppliedEnv    string    `json:"applied_env"`
	Checksum      string    `json:"checksum"`
	AppliedAt     time.Time `json:"applied_at"`
	Error         *string   `json:"error,omitempty"`
}
