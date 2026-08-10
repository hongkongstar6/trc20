package signer

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/sirupsen/logrus"
)

// NewHTTPServer exposes the signing service. The handler is deliberately tiny:
// authentication, then policy, then sign. Everything else lives elsewhere so
// this process links as little code as possible.
func InitRouter(r *gin.Engine, svc *Service, token string) {
	r.HandleMethodNotAllowed = true
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	v1 := r.Group("/v1", authorize(token))
	v1.POST("/sign", svc.Sign1)    //用私钥对交易进行签名
	v1.POST("/derive", svc.Derive) //生成专属地址

}

// v1/sign
func (svc *Service) Sign1(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)

	var req SignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	resp, err := svc.Sign(c.Request.Context(), &req, callerOf(c))
	if err != nil {
		logrus.Warn("sign rejected", "purpose", req.Purpose, "address", req.Address, ",err:", err)
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (svc *Service) Derive(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<16)
	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	addr, err := svc.DeriveAddress(req.Path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"address": addr, "path": req.Path})

}

func authorize(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if token == "" {
			c.Next()
			return
		}
		got := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// callerOf identifies the client for the audit trail: the mTLS subject when
// available, the peer address otherwise.
func callerOf(c *gin.Context) string {
	if tls := c.Request.TLS; tls != nil && len(tls.PeerCertificates) > 0 {
		return tls.PeerCertificates[0].Subject.CommonName
	}
	return c.Request.RemoteAddr
}

// PolicyFromConfig derives the signing policy from the deployment config, so
// the allowlists cannot drift away from what the workers are configured to do.
func PolicyFromConfig() Policy {
	policy := Policy{
		TopupWhitelist:    map[string]string{},
		AllowedContracts:  map[string]bool{},
		GasAccountAddress: config.Cfg.Energy.AutoTopup.SourceAddress,
		SweepDestination:  config.Cfg.Wallet.FinanceWallet.Address,
		WithdrawFrom:      config.Cfg.Wallet.HotWallet.Address,
	}
	for name, conf := range config.Cfg.Energy.AutoTopup.Providers {
		if conf.DepositAddress != "" {
			policy.TopupWhitelist[name] = conf.DepositAddress
		}
		if cap := int64(conf.MaxSingleTopupTRX * 1e6); cap > policy.TopupMaxSun {
			policy.TopupMaxSun = cap
		}
	}
	for _, token := range config.Cfg.Wallet.Tokens {
		if token.Enabled {
			policy.AllowedContracts[token.Contract] = true
		}
	}
	return policy
}

// NewDBAudit persists every signing decision, allowed or refused.
func NewDBAudit() AuditSink {
	return &dbAudit{}
}

type dbAudit struct {
	//st *store.Store
	//log *logrus.Logger
}

func (a *dbAudit) Record(ctx context.Context, purpose, path, address, txid, caller string, allowed bool, reason string) {
	row := &model.SignAudit{
		Purpose: purpose, Path: path, Address: address, TxID: txid,
		Caller: caller, Allowed: allowed, Reason: reason, CreatedAt: time.Now(),
	}
	if err := store.MyStore.DB.WithContext(ctx).Create(row).Error; err != nil {
		// Audit must never silently vanish, but it must not block signing of a
		// legitimate request either.
		logrus.Error("sign audit write failed", "purpose", purpose, "allowed", allowed, ",err:", err)
	}
}
