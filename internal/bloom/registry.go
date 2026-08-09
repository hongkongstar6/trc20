package bloom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/store"
	"github.com/sirupsen/logrus"
)

// AddrFilter is the process wide address filter. It is nil until Init runs, and
// every method is nil safe: a service that never initialised it keeps falling
// back to the database lookup instead of silently dropping deposits.
var AddrFilter *Registry

// Registry owns the filter over the user_wallet addresses. The filter is loaded
// once at startup, extended when a new address is allocated, and re-synced from
// the table by id so a scanner process also learns about addresses allocated by
// the api process.
type Registry struct {
	Mu     sync.RWMutex
	filter *BloomFilter
	maxID  int64

	pageSize int
}

func GetNew(expected uint64, fpRate float64) *Registry {
	return &Registry{filter: NewBloomFilter(expected, fpRate)}
}

// Init builds the filter and loads every user_wallet address into it.
func Init(ctx context.Context) (*Registry, error) {
	r := &Registry{
		filter:   NewBloomFilter(uint64(config.Cfg.Bloom.ExpectedAddresses), config.Cfg.Bloom.FalsePositiveRate),
		pageSize: config.Cfg.Bloom.LoadBatch,
	}
	if err := r.reload(ctx, r.filter); err != nil {
		return nil, err
	}
	AddrFilter = r
	logrus.Info("address bloom filter loaded",
		",addresses:", r.filter.Count(),
		",capacity:", r.filter.Capacity(),
		",bits:", r.filter.Bits(),
		",hashes:", r.filter.Hashes(),
		",max_id:", r.maxID)
	return r, nil
}

// Add registers a freshly allocated address so the very next block carrying a
// transfer to it is matched.
func (r *Registry) Add(address string) {
	if r == nil || address == "" {
		return
	}
	r.Mu.Lock()
	r.filter.Add(address)
	r.Mu.Unlock()
}

// MayContain is false only when the address is certainly unknown, which is the
// case for virtually every recipient seen on chain.
func (r *Registry) MayContain(address string) bool {
	if r == nil {
		return true
	}
	r.Mu.RLock()
	f := r.filter
	r.Mu.RUnlock()
	return f.MayContain(address)
}

func (r *Registry) Count() uint64 {
	if r == nil {
		return 0
	}
	r.Mu.RLock()
	defer r.Mu.RUnlock()
	return r.filter.Count()
}

// Sync loads the addresses inserted since the last call. When the filter grew
// past the size it was built for it is rebuilt from the table at twice the
// capacity, because an overloaded filter matches almost everything and would
// push the load back onto MySQL.
func (r *Registry) Sync(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.Mu.RLock()
	overloaded := r.filter.Count() > r.filter.Capacity()
	capacity := r.filter.Capacity()
	r.Mu.RUnlock()
	if overloaded {
		return r.rebuild(ctx, capacity*2)
	}
	for {
		r.Mu.RLock()
		after := r.maxID
		r.Mu.RUnlock()
		rows, err := store.UserWalletAddressesAfter(ctx, after, r.batch())
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		r.Mu.Lock()
		for i := range rows {
			r.filter.Add(rows[i].Address)
			if rows[i].ID > r.maxID {
				r.maxID = rows[i].ID
			}
		}
		r.Mu.Unlock()
		if len(rows) < r.batch() {
			return nil
		}
	}
}

// RunSync keeps the filter in sync until ctx is cancelled.
func (r *Registry) RunSync(ctx context.Context) {
	if r == nil {
		return
	}
	interval := config.Duration(config.Cfg.Bloom.SyncInterval, 10*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Sync(ctx); err != nil {
				// A stale filter only delays new addresses, so a failed sync is
				// retried on the next tick instead of stopping the scanner.
				logrus.Error("address bloom sync failed", ",err:", err)
			}
		}
	}
}

// rebuild loads the whole table into a new filter and swaps it in, so lookups
// never observe a partially filled filter.
func (r *Registry) rebuild(ctx context.Context, capacity uint64) error {
	fresh := NewBloomFilter(capacity, config.Cfg.Bloom.FalsePositiveRate)
	maxID, err := r.load(ctx, fresh)
	if err != nil {
		return err
	}
	r.Mu.Lock()
	r.filter = fresh
	r.maxID = maxID
	r.Mu.Unlock()
	logrus.Info("address bloom filter rebuilt",
		",addresses:", fresh.Count(), ",capacity:", fresh.Capacity())
	return nil
}

func (r *Registry) reload(ctx context.Context, into *BloomFilter) error {
	maxID, err := r.load(ctx, into)
	if err != nil {
		return err
	}
	r.maxID = maxID
	return nil
}

func (r *Registry) load(ctx context.Context, into *BloomFilter) (int64, error) {
	var maxID int64
	for {
		rows, err := store.UserWalletAddressesAfter(ctx, maxID, r.batch())
		if err != nil {
			return 0, err
		}
		for i := range rows {
			into.Add(rows[i].Address)
			if rows[i].ID > maxID {
				maxID = rows[i].ID
			}
		}
		if len(rows) < r.batch() {
			return maxID, nil
		}
	}
}

func (r *Registry) batch() int {
	if r.pageSize <= 0 {
		return 5000
	}
	return r.pageSize
}
func (r *Registry) GetMax() int64 {
	return r.maxID
}

//	func (r *Registry) GetRWMutex() sync.RWMutex {
//		return r.mu
//	}
func (r *Registry) GetPageSize() int {
	return r.pageSize
}
func (r *Registry) GetBloomFilter() *BloomFilter {
	return r.filter
}

type AddressRequest struct {
	Addresses []string `json:"addresses" binding:"required"`
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
