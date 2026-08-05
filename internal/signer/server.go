package signer

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
)

// NewHTTPServer exposes the signing service. The handler is deliberately tiny:
// authentication, then policy, then sign. Everything else lives elsewhere so
// this process links as little code as possible.
func NewHTTPServer(svc *Service, token string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.Handle("/v1/sign", authorize(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
			return
		}
		var req SignRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		resp, err := svc.Sign(r.Context(), &req, callerOf(r))
		if err != nil {
			log.Warn("sign rejected", "purpose", req.Purpose, "address", req.Address, "err", err)
			writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})))
	mux.Handle("/v1/derive", authorize(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		addr, err := svc.DeriveAddress(req.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"address": addr, "path": req.Path})
	})))
	return mux
}

func authorize(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// callerOf identifies the client for the audit trail: the mTLS subject when
// available, the peer address otherwise.
func callerOf(r *http.Request) string {
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		return r.TLS.PeerCertificates[0].Subject.CommonName
	}
	return r.RemoteAddr
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// PolicyFromConfig derives the signing policy from the deployment config, so
// the allowlists cannot drift away from what the workers are configured to do.
func PolicyFromConfig(cfg *config.Config) Policy {
	policy := Policy{
		TopupWhitelist:    map[string]string{},
		AllowedContracts:  map[string]bool{},
		GasAccountAddress: cfg.Energy.AutoTopup.SourceAddress,
		SweepDestination:  cfg.Wallet.FinanceWallet.Address,
		WithdrawFrom:      cfg.Wallet.HotWallet.Address,
	}
	for name, conf := range cfg.Energy.AutoTopup.Providers {
		if conf.DepositAddress != "" {
			policy.TopupWhitelist[name] = conf.DepositAddress
		}
		if cap := int64(conf.MaxSingleTopupTRX * 1e6); cap > policy.TopupMaxSun {
			policy.TopupMaxSun = cap
		}
	}
	for _, token := range cfg.Wallet.Tokens {
		if token.Enabled {
			policy.AllowedContracts[token.Contract] = true
		}
	}
	return policy
}

// NewDBAudit persists every signing decision, allowed or refused.
func NewDBAudit(st *store.Store, log *slog.Logger) AuditSink {
	return &dbAudit{st: st, log: log}
}

type dbAudit struct {
	st  *store.Store
	log *slog.Logger
}

func (a *dbAudit) Record(ctx context.Context, purpose, path, address, txid, caller string, allowed bool, reason string) {
	row := &model.SignAudit{
		Purpose: purpose, Path: path, Address: address, TxID: txid,
		Caller: caller, Allowed: allowed, Reason: reason, CreatedAt: time.Now(),
	}
	if err := a.st.DB.WithContext(ctx).Create(row).Error; err != nil {
		// Audit must never silently vanish, but it must not block signing of a
		// legitimate request either.
		a.log.Error("sign audit write failed", "purpose", purpose, "allowed", allowed, "err", err)
	}
}
