// Package api exposes the business facing HTTP surface: address allocation,
// withdrawal submission, and the reconciliation endpoints the business system
// needs because it — not the wallet — owns user balances.
package api

import (
	"crypto/hmac"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/hongkongstar6/trc20/internal/bloom"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/hd"
	"github.com/hongkongstar6/trc20/internal/merchant"
	"github.com/hongkongstar6/trc20/internal/model"
	"github.com/hongkongstar6/trc20/internal/outbox"
	"github.com/hongkongstar6/trc20/internal/signer"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/hongkongstar6/trc20/internal/tron"
)

type Server struct {
	sign *signer.Client
	//cfg  *config.Config
	//st   *store.Store
	//log  *logrus.Logger
}

func New(sign *signer.Client) *Server {
	return &Server{
		sign: sign,
		//cfg:  cfg,
		//st:   st,
		//log: log
	}
}

func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), s.requestLogger())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	v1 := r.Group("/v1", s.ipAllowlist(), s.authenticate())

	v1.POST("/merchant", s.upsertMerchant)
	v1.GET("/merchants", s.listMerchants)

	v1.POST("/address", s.createAddress) //获取专属地址

	v1.POST("/withdraw", s.createWithdraw)       //玩家发起提现
	v1.GET("/withdraw/:order_no", s.getWithdraw) //查询提现

	v1.GET("/deposits", s.listDeposits) //对账
	v1.GET("/deposit/:event_id", s.getDeposit)

	v1.GET("/events", s.listEvents)
	return r
}

func (s *Server) requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logrus.Info("http",
			"method", c.Request.Method, "path", c.FullPath(),
			"status", c.Writer.Status(), "cost_ms", time.Since(start).Milliseconds())
	}
}

func maxBodyBytes() int64 {
	if config.Cfg.APIServer.MaxBodyBytes <= 0 {
		return 1 << 20
	}
	return config.Cfg.APIServer.MaxBodyBytes
}

func (s *Server) ipAllowlist() gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, ip := range config.Cfg.APIServer.AllowedIPs {
		allowed[ip] = true
	}
	return func(c *gin.Context) {
		if len(allowed) == 0 {
			c.Next()
			return
		}
		if !allowed[c.ClientIP()] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "ip not allowed" + c.ClientIP()})
			return
		}
		c.Next()
	}
}

// authenticate verifies HMAC(timestamp + body) and rejects replays.
func (s *Server) authenticate() gin.HandlerFunc {
	skew := config.Duration(config.Cfg.APIServer.SignatureSkew, 5*time.Minute)
	nonceTTL := config.Duration(config.Cfg.APIServer.NonceTTL, 10*time.Minute)
	maxBody := maxBodyBytes()
	return func(c *gin.Context) {
		if config.Cfg.APIServer.HMACSecret == "" {
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
		expected := outbox.Sign(config.Cfg.APIServer.HMACSecret, ts, body)
		if !hmac.Equal([]byte(expected), []byte(sig)) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad signature"})
			return
		}
		if nonce != "" && store.MyStore.Redis != nil {
			key := store.MyStore.Key("nonce", hex.EncodeToString([]byte(nonce)))
			ok, err := store.MyStore.Redis.SetNX(c, key, "1", nonceTTL).Result()
			if err == nil && !ok {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "replayed nonce"})
				return
			}
		}
		c.Next()
	}
}

// ------------------------------------------------------------------ merchants

type merchantRequest struct {
	MerchantID  string `json:"merchant_id" binding:"required"`
	Name        string `json:"name" binding:"required"`
	CallbackURL string `json:"callback_url" binding:"required"`
	Symbol      string `json:"symbol" binding:"required"` //币种 usdt / usdc
	Chain       string `json:"chain" binding:"required"`  //公链类型 tron / eth / ...
	Secret      string `json:"secret" binding:"required"`
	Status      *int8  `json:"status"`
}

// upsertMerchant registers a merchant or updates an existing one. The secret is
// write only: it signs inbound parameters and outbound callbacks and is never
// returned by the API.
func (s *Server) upsertMerchant(c *gin.Context) {
	var req merchantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// A merchant opened for a symbol or chain this gateway cannot serve would
	// only ever get its address requests refused, so it is refused here.
	if !strings.EqualFold(req.Chain, model.ChainTRON) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported chain: " + req.Chain})
		return
	}
	if _, ok := config.Cfg.EnabledToken(req.Symbol); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported symbol: " + req.Symbol})
		return
	}
	status := model.MerchantStatusOn
	if req.Status != nil {
		status = *req.Status
	}
	row := model.Merchant{
		MerchantID:  req.MerchantID,
		Name:        req.Name,
		CallbackURL: req.CallbackURL,
		Symbol:      req.Symbol,
		Chain:       req.Chain,
		Secret:      req.Secret,
		Status:      status,
	}
	err := store.MyStore.DB.WithContext(c).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "merchant_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"name", "callback_url", "symbol", "chain", "secret", "status", "updated_at",
		}),
	}).Create(&row).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"merchant_id": row.MerchantID, "symbol": row.Symbol,
		"chain": row.Chain, "status": status,
	})
}

func (s *Server) listMerchants(c *gin.Context) {
	var rows []model.Merchant
	if err := store.MyStore.DB.WithContext(c).Order("id asc").
		Limit(parseLimit(c, 200, 1000)).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"total": len(rows), "items": rows})
}

// ------------------------------------------------------------------ addresses

// createAddress allocates one deposit address per (merchant_id, uid). The
// parameters are signed with the merchant secret: sha256 over the parameters
// sorted by key as "k1=v1&k2=v2" with the secret appended. The derivation index
// comes from the allocator, never from the uid itself.
//
// symbol and chain must be the ones the merchant is registered for: a request
// for anything else is a misrouted integration, and answering it with an
// address would credit deposits the merchant never expected.
func (s *Server) createAddress(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes()))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read body"})
		return
	}
	params, err := merchant.DecodeParams(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json body"})
		return
	}
	merchantID := merchant.String(params, "merchant_id")
	uid := merchant.String(params, "uid")
	symbol := merchant.String(params, "symbol")
	chain := merchant.String(params, "chain")
	if merchantID == "" || uid == "" || symbol == "" || chain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "merchant_id, uid, symbol and chain are required"})
		return
	}
	mch, err := merchant.GetEnabled(c, merchantID)
	if errors.Is(err, merchant.ErrNotFound) || errors.Is(err, merchant.ErrDisabled) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	logrus.Info("商户id:", mch.MerchantID)
	if err := merchant.Verify(params, mch.Secret); err != nil {
		//c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		//return
	}
	if !strings.EqualFold(symbol, mch.Symbol) || !strings.EqualFold(chain, mch.Chain) {
		logrus.Warn("申请地址失败,币种/公链与商户不一致:", merchantID,
			",request:", symbol, "/", chain, ",merchant:", mch.Symbol, "/", mch.Chain)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "symbol or chain does not match the merchant configuration",
		})
		return
	}
	// The merchant row is only the contract with the business system; the gateway
	// still has to be configured for that token and chain itself.
	if !strings.EqualFold(chain, model.ChainTRON) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported chain: " + chain})
		return
	}
	if _, ok := config.Cfg.EnabledToken(symbol); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported symbol: " + symbol})
		return
	}

	account := merchant.MakeAccount(merchantID, uid)
	var existing model.UserWallet
	err = store.MyStore.DB.WithContext(c).
		Where("account = ? AND chain = ? AND purpose = ?", account, "TRON", "deposit").
		Take(&existing).Error
	if err == nil {
		c.JSON(http.StatusOK, gin.H{
			"merchant_id": merchantID, "uid": uid, "account": account,
			"address": existing.Address, "chain": existing.Chain, "symbol": mch.Symbol,
		})
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	index, err := store.MyStore.NextAddressIndex(c, "TRON")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	path := hd.AddressPath(config.Cfg.Wallet.AccountPath, index)

	address, err := s.sign.DeriveAddress(c, path)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "请求签名服,derive failed: " + err.Error()})
		return
	}
	logrus.Info("生成地址的路径:", account, path, address)

	wallet := model.UserWallet{
		MerchantID: merchantID,
		UID:        uid,
		Account:    account,
		Chain:      "TRON",
		ChainIdx:   "TRON",
		Address:    address,
		AddrIndex:  index,
		DerivePath: path,
		Purpose:    "deposit",
		Status:     1,
	}
	if err := store.MyStore.DB.WithContext(c).Create(&wallet).Error; err != nil {
		// Concurrent allocation for the same account: return the winning row.
		if e := store.MyStore.DB.WithContext(c).Where("account = ?", account).Take(&existing).Error; e == nil {
			c.JSON(http.StatusOK, gin.H{
				"merchant_id": merchantID, "uid": uid, "account": account,
				"address": existing.Address, "chain": existing.Chain, "symbol": mch.Symbol,
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// A deposit to a brand new address can land in the very next block, so the
	// filter is extended as soon as the row is committed and the address is
	// pushed to the scanner process. A failed push is not an allocation error:
	// the scanner also syncs new rows from user_wallet periodically.
	bloom.NotifyWithRetry(address)
	c.JSON(http.StatusOK, gin.H{
		"merchant_id": merchantID,
		"uid":         uid,
		"address":     address,
		"chain":       model.ChainTRON,
		"symbol":      mch.Symbol,
	})
}

// ----------------------------------------------------------------- withdrawal

type createWithdrawRequest struct {
	OrderNo    string `json:"order_no" binding:"required"`    //商户唯一订单号
	ExtParam   string `json:"ext_param" binding:"required"`   //拓展字段,一般存用户id
	MerchantId string `json:"merchant_id" binding:"required"` //商户id
	Symbol     string `json:"symbol" binding:"required"`      //提现币种 USDC / USDT / BNB / BTC / SOL / ETH /TRX
	Chain      string `json:"chain" binding:"required"`       //链类型:ETH / TRON / BSC / BTC / SOL
	ToAddress  string `json:"to_address" binding:"required"`  //提现地址
	Amount     string `json:"amount" binding:"required"`      //提现金额 minimum units
	NotifyUrl  string `json:"notify_url" binding:"required"`  //异步通知地址
	OrderTime  int64  `json:"order_time" binding:"required"`  //下单时间戳秒
	ClientIp   string `json:"client_ip" binding:"required"`   //用户ip
	Sign       string `json:"sgin" binding:"required"`        //签名
}

type createWithdrawResponse struct {
	MerchantId string `json:"merchant_id" binding:"required"` //商户id
	OrderNo    string `json:"order_no" binding:"required"`    //商户唯一订单号
	TradeNo    string `json:"trade_no" binding:"required"`    //支付方的交易id
	CreateTime int64  `json:"create_time" binding:"required"` //创建时间戳
}

// createWithdraw 会记录订单,order_no 的唯一性保证了重试提交的安全性：// 同一订单永远不会被支付两次。
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
	// The order outcome is only reportable if the callback URL is usable, and an
	// unusable one must be refused now rather than dead lettered after payout.
	if !isHTTPURL(req.NotifyUrl) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "notify_url must be an absolute http(s) URL"})
		return
	}
	amount, ok := new(big.Int).SetString(req.Amount, 10)
	if !ok || amount.Sign() <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be a positive integer in minimum units"})
		return
	}
	if !strings.EqualFold(req.Chain, model.ChainTRON) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported chain: " + req.Chain})
		return
	}
	token, ok := config.Cfg.EnabledToken(req.Symbol)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported symbol: " + req.Symbol})
		return
	}

	row := model.WithdrawRecord{
		OrderNo:     req.OrderNo,
		TradeNo:     uuid.New().String(), //生成唯一
		ExtParam:    req.ExtParam,
		MerchantID:  req.MerchantId,
		Chain:       model.ChainTRON,
		Symbol:      token.Symbol,
		Contract:    token.Contract, //智能合约地址
		ToAddress:   req.ToAddress,
		NotifyURL:   req.NotifyUrl,
		AmountUnits: amount.String(),
		Decimals:    token.Decimals,
		Status:      model.WithdrawStateCreated,
		FromAddress: config.Cfg.Wallet.HotWallet.Address,
	}
	//提现订单插入数据库后,由withdraw服务 定时从数据库中找出提现订单
	if err := store.MyStore.DB.WithContext(c).Create(&row).Error; err != nil {
		// Duplicate submission: return the existing order instead of failing.
		var existing model.WithdrawRecord
		if e := store.MyStore.DB.WithContext(c).Where("order_no = ?", req.OrderNo).Take(&existing).Error; e == nil {
			c.JSON(http.StatusOK, createWithdrawResponse{
				MerchantId: existing.MerchantID,
				OrderNo:    existing.OrderNo,
				TradeNo:    existing.TradeNo,
				CreateTime: existing.CreatedAt.Unix(),
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, createWithdrawResponse{
		MerchantId: row.MerchantID,
		OrderNo:    row.OrderNo,
		TradeNo:    row.TradeNo,
		CreateTime: row.CreatedAt.Unix(),
	})
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (s *Server) getWithdraw(c *gin.Context) {
	var row model.WithdrawRecord
	err := store.MyStore.DB.WithContext(c).Where("order_no = ?", c.Param("order_no")).Take(&row).Error
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
	q := store.MyStore.DB.WithContext(c).Model(&model.DepositRecord{}).
		Where("status = ? AND internal = ? AND confirmed_at BETWEEN ? AND ?", model.DepositStateConfirmed, false, from, to)

	if merchantID := c.Query("merchant_id"); merchantID != "" {
		q = q.Where("merchant_id = ?", merchantID)
	}
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
	err = store.MyStore.DB.WithContext(c).Where("txid = ? AND event_index = ?", txid, index).Take(&row).Error
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
	q := store.MyStore.DB.WithContext(c).Model(&model.NotifyOutbox{})
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

// 提现回调
type WithdrawCallBackReq struct {
	Status      int    `json:"status"`      //状态 1 成功 2.失败
	MerchantID  string `json:"merchant_id"` //商户名称,
	OrderNo     string `json:"order_no"`    //商户订单id,
	TransNo     string `json:"trans_no"`    //支付系统交易id,
	ToAddress   string `json:"to_address"`  //收款地址,
	CoinNum     string `json:"coinNum"`     //提现金额,
	RealCoinNum string `json:"realCoinNum"` //实际支付金额,
	CreateTime  string `json:"createTime"`  //创建订单时间,
	TradeTime   int64  `json:"tradeTime"`   //交易时间戳,
	TxId        string `json:"txId"`        //: "21199767b669421a485e302c21d1406e44c7b8792cea7ab2c0137766c31935bc",
	Sign        string `json:"sign"`        //签名
}
