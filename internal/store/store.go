// Package store owns database access: connection setup, the address index
// allocator, and the transactional outbox writer.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/hongkongstar6/trc20/internal/config"
	"github.com/hongkongstar6/trc20/internal/model"
)

type Store struct {
	DB     *gorm.DB
	Redis  *redis.Client
	prefix string
}

var MyStore *Store

func Open() (*Store, error) {
	a := mysql.Open(config.Cfg.MySQL.DSN)

	gdb, err := gorm.Open(a, &gorm.Config{
		// "record not found" is an expected outcome for most lookups here, so
		// it must not drown out the warnings that matter.
		Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("store: open mysql: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	if config.Cfg.MySQL.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(config.Cfg.MySQL.MaxOpenConns)
	}
	if config.Cfg.MySQL.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(config.Cfg.MySQL.MaxIdleConns)
	}
	sqlDB.SetConnMaxLifetime(config.Duration(config.Cfg.MySQL.ConnMaxLifetime, time.Hour))

	s := &Store{DB: gdb, prefix: config.Cfg.Redis.Prefix}
	if config.Cfg.Redis.Addr != "" {
		s.Redis = redis.NewClient(&redis.Options{
			Addr:     config.Cfg.Redis.Addr,
			Password: config.Cfg.Redis.Password,
			DB:       config.Cfg.Redis.DB,
		})
	}
	if s.prefix == "" {
		s.prefix = "trc20"
	}
	MyStore = s
	return s, nil
}

// AutoMigrate is a convenience for dev/test. Production uses migrations/*.sql.
func (s *Store) AutoMigrate() error {
	return s.DB.AutoMigrate(model.AllModels()...)
}

func (s *Store) Key(parts ...string) string {
	k := s.prefix
	for _, p := range parts {
		k += ":" + p
	}
	return k
}

// Lock acquires a redis lock. Sweeps and topups must never run concurrently
// for the same address, otherwise the same funds are spent twice.
func (s *Store) Lock(ctx context.Context, key string, ttl time.Duration) (func(), bool) {
	if s.Redis == nil {
		return func() {}, true
	}
	full := s.Key("lock", key)
	ok, err := s.Redis.SetNX(ctx, full, "1", ttl).Result()
	if err != nil || !ok {
		return nil, false
	}
	return func() { s.Redis.Del(context.Background(), full) }, true
}

// ------------------------------------------------------------- index allocator

// NextAddressIndex atomically reserves the next derivation index for a chain.
// The uid is deliberately not used as the index.
// 表wallet_index_allocator 记录路径index
func (s *Store) NextAddressIndex(ctx context.Context, chain string) (int64, error) {
	var index int64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var alloc model.WalletIndexAllocator
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("chain = ?", chain).Take(&alloc).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			alloc = model.WalletIndexAllocator{Chain: chain, NextIndex: 0}
			if err := tx.Create(&alloc).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		index = alloc.NextIndex
		return tx.Model(&model.WalletIndexAllocator{}).
			Where("chain = ?", chain).
			UpdateColumns(map[string]any{"next_index": index + 1, "updated_at": time.Now()}).Error
	})
	return index, err
}

// ------------------------------------------------------------------- outbox

// EnqueueOutbox writes an event inside the caller's transaction so the state
// change and the notification commit atomically.
// merchantID may be empty for platform level events; when set the dispatcher
// also delivers the event to that merchant's callback URL.
func EnqueueOutbox(tx *gorm.DB, eventID, eventType, merchantID string, payload any) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	row := model.NotifyOutbox{
		EventID:    eventID,
		EventType:  eventType,
		MerchantID: merchantID,
		Payload:    string(blob),
		Status:     model.OutboxStatePending,
		NextRetry:  time.Now(),
	}
	// Duplicate event ids are expected on replay and must not be an error.
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

func UserWalletsByAddresses(ctx context.Context, addresses []string, wallets *[]model.UserWallet) error {
	err := MyStore.DB.WithContext(ctx).
		Where("address IN ? AND purpose = ?", addresses, "deposit").Find(wallets).Error
	return err
}

// UserWalletAddressesAfter pages over user_wallet ordered by id, reading only
// the two columns the address filter needs. afterID = 0 starts from the first
// row, so the same call does both the startup load and the incremental sync.
func UserWalletAddressesAfter(ctx context.Context, afterID int64, limit int) ([]model.UserWallet, error) {
	var rows []model.UserWallet
	err := MyStore.DB.WithContext(ctx).Model(&model.UserWallet{}).
		Select("id", "address").
		Where("id > ?", afterID).
		Order("id asc").Limit(limit).Find(&rows).Error
	return rows, err
}

func IsInternalWallet(ctx context.Context, row *model.WithdrawRecord, internal *int64) error {
	//var internal int64
	err := MyStore.DB.WithContext(ctx).Model(&model.UserWallet{}).
		Where("address = ?", row.ToAddress).Count(internal).Error

	return err
}
