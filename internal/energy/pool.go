package energy

import (
	"context"
	"fmt"
	"time"

	"github.com/hongkongstar6/trc20/internal/chain"
	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/sirupsen/logrus"
)

// Pool keeps delegated energy on the withdrawal hot wallet.
//
// The hot wallet is a single fixed address doing roughly 200 withdrawals a day,
// so renting per withdrawal means 200 orders, 200 waits and 200 small-order
// surcharges. Instead we rent in batches when the available energy drops below
// a low water mark, which turns ~200 orders/day into ~20 and makes withdrawals
// wait for nothing.
type Pool struct {
	cfg    config.EnergyPoolConfig
	energy config.EnergyConfig
	mgr    *Manager
	gw     *chain.Gateway
	//log    *logrus.Logger
	addr string

	lastHourUsed int64
	hourStart    time.Time
}

func NewPool(cfg config.EnergyConfig, mgr *Manager, gw *chain.Gateway, log *logrus.Logger, hotWallet string) *Pool {
	return &Pool{cfg: cfg.Pool, energy: cfg, mgr: mgr, gw: gw,
		//log: log,
		addr: hotWallet, hourStart: time.Now()}
}

func (p *Pool) Run(ctx context.Context) error {
	if !p.cfg.Enabled {
		logrus.Info("hot wallet energy pool disabled")
		return nil
	}
	interval := config.Duration(p.cfg.CheckInterval, 30*time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.topUp(ctx); err != nil {
			logrus.Error("energy pool top up failed", ",err:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// topUp rents a batch when the pool falls below the low water mark.
func (p *Pool) topUp(ctx context.Context) error {
	res, err := p.gw.GetAccountResource(ctx, p.addr)
	if err != nil {
		return err
	}
	perTx := p.energy.EnergyPerTxNew
	available := res.AvailableEnergy()
	lowWater := p.cfg.LowWaterTxs * perTx
	if available >= lowWater {
		return nil
	}
	batchTxs := p.batchSize()
	need := batchTxs*perTx - available
	if need <= 0 {
		return nil
	}
	requestID := fmt.Sprintf("pool-%s-%d", p.addr[len(p.addr)-6:], time.Now().UnixMilli())
	logrus.Info("renting hot wallet energy batch",
		"available", available, "low_water", lowWater, "batch_txs", batchTxs, "need", need)

	// Never fall back to burning TRX: an unnoticed burn costs several times the
	// rental price per withdrawal, so a rental outage has to be alerted on and
	// leaves the withdrawals waiting.
	order, err := p.mgr.AcquireRented(ctx, "hot_pool", p.addr, need, requestID)
	if err != nil {
		logrus.Error("ALERT hot wallet energy rental failed, withdrawals are stopped until it recovers",
			"address", p.addr, "need", need, ",err:", err)
		return err
	}
	logrus.Info("hot wallet energy batch delegated",
		"provider", order.Provider, "energy", order.RequestedEnergy, "cost_trx", order.CostTRX)
	return nil
}

// batchSize adapts to the previous hour's actual consumption so quiet nights do
// not pay for energy nobody uses.
func (p *Pool) batchSize() int64 {
	batch := p.cfg.BatchTxs
	if batch <= 0 {
		batch = 10
	}
	if time.Since(p.hourStart) >= time.Hour {
		p.hourStart = time.Now()
		p.lastHourUsed = 0
	}
	if p.lastHourUsed > 0 && p.lastHourUsed < batch {
		batch = p.lastHourUsed
	}
	if p.cfg.MaxBatchTxs > 0 && batch > p.cfg.MaxBatchTxs {
		batch = p.cfg.MaxBatchTxs
	}
	if batch < 1 {
		batch = 1
	}
	return batch
}

// RecordUsage is called by withdraw-worker after each broadcast so the batch
// size tracks real demand.
func (p *Pool) RecordUsage(txs int64) {
	if time.Since(p.hourStart) >= time.Hour {
		p.hourStart = time.Now()
		p.lastHourUsed = 0
	}
	p.lastHourUsed += txs
}

// HasEnergyFor reports whether the pool can cover one more transaction, which
// lets withdraw-worker decide between using the pool and burning TRX.
func (p *Pool) HasEnergyFor(ctx context.Context, need int64) bool {
	res, err := p.gw.GetAccountResource(ctx, p.addr)
	if err != nil {
		return false
	}
	return res.AvailableEnergy() >= need
}
