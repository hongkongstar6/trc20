package bloom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	//"github.com/hongkongstar6/trc20/internal/bloom"

	"github.com/hongkongstar6/trc20/internal/config"
)

// The api process allocates addresses, the scanner process matches them, so a
// new address is pushed over http instead of waiting for the next sync tick.

const addressPath = "/internal/bloom/address"

// type AddressRequest struct {
// 	Addresses []string `json:"addresses" binding:"required"`
// }

// Serve exposes the filter of this process so another process can push newly
// allocated addresses into it. Adding an address is idempotent, so a retried
// or duplicated push is harmless.
func Serve(ctx context.Context) error {
	// if r == nil || config.Cfg.Bloom.Listen == "" {
	// 	return nil
	// }

	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.POST("/internal/bloom/address", BloomAddAddress)
	engine.GET("/internal/bloom/stats", BloomState)
	//engine.Run()
	srv := &http.Server{Addr: config.Cfg.Bloom.Listen, Handler: engine}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	logrus.Info("bloom address sync listening", ",addr:", config.Cfg.Bloom.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
func BloomState(c *gin.Context) {
	AddrFilter.Mu.RLock()
	f, maxID := AddrFilter.GetBloomFilter(), AddrFilter.GetMax()
	AddrFilter.Mu.RUnlock()
	c.JSON(http.StatusOK, gin.H{
		"addresses": f.Count(), "capacity": f.Capacity(),
		"bits": f.Bits(), "hashes": f.Hashes(),
		"false_positive_rate": f.FalsePositiveRate(), "max_id": maxID,
	})
}

func BloomAddAddress(c *gin.Context) {
	token := config.Cfg.Bloom.Token
	if token != "" && c.GetHeader("X-Bloom-Token") != token {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "bad token"})
		return
	}
	var req AddressRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	added := 0
	for _, a := range req.Addresses {
		if a == "" {
			continue
		}
		AddrFilter.Add(a)
		added++
	}
	logrus.Info("address pushed into bloom filter", ",added:", added, ",total:", AddrFilter.Count())
	c.JSON(http.StatusOK, gin.H{"added": added, "total": AddrFilter.Count()})
}

// Notify pushes addresses to the scanner process. It is best effort on purpose:
// the caller must not fail the allocation because the scanner is momentarily
// down, the periodic Sync picks the address up in that case.
func Notify(ctx context.Context, addresses ...string) error {
	url := config.Cfg.Bloom.BloomNotifyURL

	if url == "" || len(addresses) == 0 {
		return nil
	}
	body, err := json.Marshal(AddressRequest{Addresses: addresses})
	if err != nil {
		return err
	}
	timeout := config.Duration(config.Cfg.Bloom.NotifyTimeout, 3*time.Second)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if config.Cfg.Bloom.Token != "" {
		req.Header.Set("X-Bloom-Token", config.Cfg.Bloom.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bloom notify: %s", resp.Status)
	}
	return nil
}
