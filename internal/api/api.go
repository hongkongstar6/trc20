// Package api exposes the business facing HTTP surface: address allocation,
// withdrawal submission, and the reconciliation endpoints the business system
// needs because it — not the wallet — owns user balances.
package api

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/hd"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/outbox"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/tron"
)

type Server struct {
	cfg  *config.Config
	st   *store.Store
	sign *signer.Client
	log  *slog.Logger
}

func New(cfg *config.Config, st *store.Store, sign *signer.Client, log *slog.Logger) *Server {
	return &Server{cfg: cfg, st: st, sign: sign, log: log}
}

func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), s.requestLogger())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	v1 := r.Group("/v1", s.ipAllowlist(), s.authenticate())
	v1.POST("/address", s.createAddress)
	v1.POST("/withdraw", s.createWithdraw)
	v1.GET("/withdraw/:biz_order_no", s.getWithdraw)
	v1.GET("/deposits", s.listDeposits)
	v1.GET("/deposit/:event_id", s.getDeposit)
	v1.GET("/events", s.listEvents)
	return r
}

func (s *Server) requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		s.log.Info("http",
			"method", c.Request.Method, "path", c.FullPath(),
			"status", c.Writer.Status(), "cost_ms", time.Since(start).Milliseconds())
	}
}

func (s *Server) ipAllowlist() gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, ip := range s.cfg.API.AllowedIPs {
		allowed[ip] = true
	}
	return func(c *gin.Context) {
		if len(allowed) == 0 {
			c.Next()
			return
		}
		if !allowed[c.ClientIP()] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "ip not allowed"})
			return
		}
		c.Next()
	}
}

// authenticate verifies HMAC(timestamp + body) and rejects replays.
func (s *Server) authenticate() gin.HandlerFunc {
	skew := config.Duration(s.cfg.API.SignatureSkew, 5*time.Minute)
	nonceTTL := config.Duration(s.cfg.API.NonceTTL, 10*time.Minute)
	maxBody := s.cfg.API.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	return func(c *gin.Context) {
		if s.cfg.API.HMACSecret == "" {
			c.Next()
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "cannot read body"})
			return
		}
		c.Request.Body = io.NopCloser(newReader(body))

		ts := c.GetHeader("X-Timestamp")
		sig := c.GetHeader("X-Signature")
		nonce := c.GetHeader("X-Nonce")
		unix, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || time.Since(time.Unix(unix, 0)).Abs() > skew {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "stale timestamp"})
			return
		}
		expected := outbox.Sign(s.cfg.API.HMACSecret, ts, body)
		if !hmac.Equal([]byte(expected), []byte(sig)) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad signature"})
			return
		}
		if nonce != "" && s.st.Redis != nil {
			key := s.st.Key("nonce", hex.EncodeToString([]byte(nonce)))
			ok, err := s.st.Redis.SetNX(c, key, "1", nonceTTL).Result()
			if err == nil && !ok {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "replayed nonce"})
				return
			}
		}
		c.Next()
	}
}

// ------------------------------------------------------------------ addresses

type createAddressRequest struct {
	UID int64 `json:"uid" binding:"required"`
}

// createAddress allocates one deposit address per uid. The derivation index
// comes from the allocator, never from the uid itself.
func (s *Server) createAddress(c *gin.Context) {
	var req createAddressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var existing model.Wallet
	err := s.st.DB.WithContext(c).
		Where("uid = ? AND chain = ? AND purpose = ?", req.UID, "TRON", "deposit").
		Take(&existing).Error
	if err == nil {
		c.JSON(http.StatusOK, gin.H{"uid": req.UID, "address": existing.Address, "chain": existing.Chain})
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	index, err := s.st.NextAddressIndex(c, "TRON")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	path := hd.AddressPath(s.cfg.Wallet.AccountPath, index)
	address, err := s.sign.DeriveAddress(c, path)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "derive failed: " + err.Error()})
		return
	}
	wallet := model.Wallet{
		UID:        req.UID,
		Chain:      "TRON",
		ChainIdx:   "TRON",
		Address:    address,
		AddrIndex:  index,
		DerivePath: path,
		Purpose:    "deposit",
		Status:     1,
	}
	if err := s.st.DB.WithContext(c).Create(&wallet).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"uid": req.UID, "address": address, "chain": "TRON"})
}

// ----------------------------------------------------------------- withdrawal

type createWithdrawRequest struct {
	BizOrderNo string `json:"biz_order_no" binding:"required"`
	UID        int64  `json:"uid" binding:"required"`
	Symbol     string `json:"symbol" binding:"required"`
	ToAddress  string `json:"to_address" binding:"required"`
	Amount     string `json:"amount" binding:"required"` // minimum units
}

// createWithdraw 会记录订单,biz_order_no 的唯一性保证了重试提交的安全性：// 同一订单永远不会被支付两次。
func (s *Server) createWithdraw(c *gin.Context) {
	var req createWithdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !tron.IsValidAddress(req.ToAddress) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid to_address"})
		return
	}
	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be a positive integer in minimum units"})
		return
	}
	var token *config.TokenConfig
	for i := range s.cfg.Wallet.Tokens {
		if s.cfg.Wallet.Tokens[i].Enabled && s.cfg.Wallet.Tokens[i].Symbol == req.Symbol {
			token = &s.cfg.Wallet.Tokens[i]
			break
		}
	}
	if token == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported symbol"})
		return
	}
	row := model.WithdrawRecord{
		BizOrderNo:  req.BizOrderNo,
		UID:         req.UID,
		Chain:       "TRON",
		Symbol:      token.Symbol,
		Contract:    token.Contract,
		ToAddress:   req.ToAddress,
		AmountUnits: amount.String(),
		Decimals:    token.Decimals,
		Status:      model.WithdrawStateCreated,
		FromAddress: s.cfg.Wallet.HotWallet.Address,
	}
	if err := s.st.DB.WithContext(c).Create(&row).Error; err != nil {
		// Duplicate submission: return the existing order instead of failing.
		var existing model.WithdrawRecord
		if e := s.st.DB.WithContext(c).Where("biz_order_no = ?", req.BizOrderNo).Take(&existing).Error; e == nil {
			c.JSON(http.StatusOK, gin.H{"biz_order_no": existing.BizOrderNo, "status": existing.Status, "txid": existing.TxID, "duplicated": true})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"biz_order_no": row.BizOrderNo, "status": row.Status})
}

func (s *Server) getWithdraw(c *gin.Context) {
	var row model.WithdrawRecord
	err := s.st.DB.WithContext(c).Where("biz_order_no = ?", c.Param("biz_order_no")).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, row)
}

// -------------------------------------------------------------- reconciliation

// listDeposits is the reconciliation endpoint: the business system can replay a
// time range at any time to verify it credited every confirmed deposit.
func (s *Server) listDeposits(c *gin.Context) {
	from, to, err := parseRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit := parseLimit(c, 200, 1000)
	q := s.st.DB.WithContext(c).Model(&model.DepositRecord{}).
		Where("status = ? AND internal = ? AND confirmed_at BETWEEN ? AND ?",
			model.DepositStateConfirmed, false, from, to)
	if uid := c.Query("uid"); uid != "" {
		q = q.Where("uid = ?", uid)
	}
	var rows []model.DepositRecord
	if err := q.Order("id asc").Limit(limit).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"total": len(rows), "items": rows})
}

func (s *Server) getDeposit(c *gin.Context) {
	eventID := c.Param("event_id")
	txid, index, err := splitEventID(eventID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var row model.DepositRecord
	err = s.st.DB.WithContext(c).Where("txid = ? AND event_index = ?", txid, index).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, row)
}

// listEvents exposes the outbox itself, including dead lettered events, so
// nothing can be silently lost between the two systems.
func (s *Server) listEvents(c *gin.Context) {
	q := s.st.DB.WithContext(c).Model(&model.NotifyOutbox{})
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if eventType := c.Query("type"); eventType != "" {
		q = q.Where("event_type = ?", eventType)
	}
	var rows []model.NotifyOutbox
	if err := q.Order("id asc").Limit(parseLimit(c, 200, 1000)).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"total": len(rows), "items": rows})
}
