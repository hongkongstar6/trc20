-- Initial schema for the USDT-TRC20 wallet gateway.
--
-- Apply this instead of relying on AutoMigrate in production: the uniqueness
-- constraints below are the load bearing part of the design and must never be
-- dropped by an accidental migration.
--   * wallet.address                one row per on-chain address
--   * wallet(chain_idx,addr_index)  one derivation index is used once
--   * deposit_record(txid,event_index)  the same Transfer log is credited once
--   * withdraw_record.biz_order_no  one business order pays out at most once
--   * energy_rent_order.request_id  no double rental after a timeout
--   * topup_record.request_id       no double provider refill after a timeout
--   * notify_outbox.event_id        at-least-once delivery, deduplicated downstream

CREATE TABLE IF NOT EXISTS `wallet` (
  `id`          BIGINT       NOT NULL AUTO_INCREMENT,
  `uid`         BIGINT       NOT NULL DEFAULT 0,
  `chain`       VARCHAR(16)  NOT NULL DEFAULT 'TRON',
  `address`     VARCHAR(64)  NOT NULL,
  `addr_index`  BIGINT       NOT NULL DEFAULT 0,
  `chain_idx`   VARCHAR(16)  NOT NULL DEFAULT 'TRON',
  `derive_path` VARCHAR(64)  NOT NULL DEFAULT '',
  `purpose`     VARCHAR(16)  NOT NULL DEFAULT 'deposit',
  `status`      TINYINT      NOT NULL DEFAULT 1,
  `created_at`  DATETIME(3)  NULL,
  `updated_at`  DATETIME(3)  NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_wallet_address` (`address`),
  UNIQUE KEY `uq_chain_index` (`chain_idx`, `addr_index`),
  KEY `idx_wallet_uid` (`uid`),
  KEY `idx_wallet_purpose` (`purpose`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Address indexes come from this allocator, never from the uid: deriving from
-- a uid leaks the user id into the derivation path and cannot be re-keyed.
CREATE TABLE IF NOT EXISTS `wallet_index_allocator` (
  `chain`      VARCHAR(16) NOT NULL,
  `next_index` BIGINT      NOT NULL DEFAULT 0,
  `updated_at` DATETIME(3) NULL,
  PRIMARY KEY (`chain`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `deposit_record` (
  `id`            BIGINT         NOT NULL AUTO_INCREMENT,
  `uid`           BIGINT         NOT NULL DEFAULT 0,
  `chain`         VARCHAR(16)    NOT NULL DEFAULT 'TRON',
  `symbol`        VARCHAR(16)    NOT NULL DEFAULT '',
  `contract`      VARCHAR(64)    NOT NULL DEFAULT '',
  `txid`          VARCHAR(70)    NOT NULL,
  `event_index`   INT            NOT NULL DEFAULT 0,
  `block_number`  BIGINT         NOT NULL DEFAULT 0,
  `block_hash`    VARCHAR(70)    NOT NULL DEFAULT '',
  `from_address`  VARCHAR(64)    NOT NULL DEFAULT '',
  `to_address`    VARCHAR(64)    NOT NULL DEFAULT '',
  -- Token amounts are stored in integer minimum units, never as float.
  `amount_units`  DECIMAL(38,0)  NOT NULL DEFAULT 0,
  `decimals`      INT            NOT NULL DEFAULT 6,
  `confirmations` BIGINT         NOT NULL DEFAULT 0,
  `status`        VARCHAR(16)    NOT NULL DEFAULT 'pending',
  `internal`      TINYINT(1)     NOT NULL DEFAULT 0,
  `swept`         TINYINT(1)     NOT NULL DEFAULT 0,
  `block_time`    DATETIME(3)    NULL,
  `confirmed_at`  DATETIME(3)    NULL,
  `created_at`    DATETIME(3)    NULL,
  `updated_at`    DATETIME(3)    NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_tx_event` (`txid`, `event_index`),
  KEY `idx_deposit_uid` (`uid`),
  KEY `idx_deposit_to` (`to_address`),
  KEY `idx_deposit_status` (`status`),
  KEY `idx_deposit_swept` (`swept`),
  KEY `idx_deposit_block` (`block_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `withdraw_record` (
  `id`           BIGINT        NOT NULL AUTO_INCREMENT,
  `biz_order_no` VARCHAR(64)   NOT NULL,
  `uid`          BIGINT        NOT NULL DEFAULT 0,
  `chain`        VARCHAR(16)   NOT NULL DEFAULT 'TRON',
  `symbol`       VARCHAR(16)   NOT NULL DEFAULT '',
  `contract`     VARCHAR(64)   NOT NULL DEFAULT '',
  `from_address` VARCHAR(64)   NOT NULL DEFAULT '',
  `to_address`   VARCHAR(64)   NOT NULL DEFAULT '',
  `amount_units` DECIMAL(38,0) NOT NULL DEFAULT 0,
  `decimals`     INT           NOT NULL DEFAULT 6,
  `status`       VARCHAR(16)   NOT NULL DEFAULT 'created',
  `fail_reason`  VARCHAR(255)  NOT NULL DEFAULT '',
  `txid`         VARCHAR(70)   NOT NULL DEFAULT '',
  -- The exact signed transaction. A retry rebroadcasts these bytes; a new
  -- transaction is only built after expiration and only when the txid is
  -- provably absent from the chain.
  `signed_raw`   TEXT          NULL,
  `expired_at`   DATETIME(3)   NULL,
  `energy_used`  BIGINT        NOT NULL DEFAULT 0,
  `fee_sun`      BIGINT        NOT NULL DEFAULT 0,
  `broadcast_at` DATETIME(3)   NULL,
  `confirmed_at` DATETIME(3)   NULL,
  `created_at`   DATETIME(3)   NULL,
  `updated_at`   DATETIME(3)   NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_withdraw_biz_order` (`biz_order_no`),
  KEY `idx_withdraw_uid` (`uid`),
  KEY `idx_withdraw_to` (`to_address`),
  KEY `idx_withdraw_status` (`status`),
  KEY `idx_withdraw_txid` (`txid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `sweep_record` (
  `id`           BIGINT        NOT NULL AUTO_INCREMENT,
  `from_address` VARCHAR(64)   NOT NULL DEFAULT '',
  `to_address`   VARCHAR(64)   NOT NULL DEFAULT '',
  `symbol`       VARCHAR(16)   NOT NULL DEFAULT '',
  `contract`     VARCHAR(64)   NOT NULL DEFAULT '',
  `amount_units` DECIMAL(38,0) NOT NULL DEFAULT 0,
  `status`       VARCHAR(16)   NOT NULL DEFAULT 'created',
  `txid`         VARCHAR(70)   NOT NULL DEFAULT '',
  `signed_raw`   TEXT          NULL,
  -- rent:<provider> or burn, kept for cost accounting per sweep.
  `fee_mode`     VARCHAR(24)   NOT NULL DEFAULT '',
  `energy_order` VARCHAR(64)   NOT NULL DEFAULT '',
  `cost_trx`     DOUBLE        NOT NULL DEFAULT 0,
  `energy_used`  BIGINT        NOT NULL DEFAULT 0,
  `fail_reason`  VARCHAR(255)  NOT NULL DEFAULT '',
  `broadcast_at` DATETIME(3)   NULL,
  `confirmed_at` DATETIME(3)   NULL,
  `created_at`   DATETIME(3)   NULL,
  `updated_at`   DATETIME(3)   NULL,
  PRIMARY KEY (`id`),
  KEY `idx_sweep_from` (`from_address`),
  KEY `idx_sweep_status` (`status`),
  KEY `idx_sweep_txid` (`txid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `energy_rent_order` (
  `id`                BIGINT      NOT NULL AUTO_INCREMENT,
  `provider`          VARCHAR(32) NOT NULL DEFAULT '',
  -- Our own key. Neither platform offers request level idempotency, so the row
  -- is written before the order is placed and reconciled by lookup afterwards.
  `request_id`        VARCHAR(64) NOT NULL,
  `provider_order_id` VARCHAR(64) NOT NULL DEFAULT '',
  `receive_address`   VARCHAR(64) NOT NULL DEFAULT '',
  `resource_type`     VARCHAR(16) NOT NULL DEFAULT 'energy',
  `requested_energy`  BIGINT      NOT NULL DEFAULT 0,
  `delegated_energy`  BIGINT      NOT NULL DEFAULT 0,
  `period`            VARCHAR(16) NOT NULL DEFAULT '',
  `cost_trx`          DOUBLE      NOT NULL DEFAULT 0,
  `status`            VARCHAR(16) NOT NULL DEFAULT 'created',
  `provider_status`   VARCHAR(32) NOT NULL DEFAULT '',
  `delegate_txid`     VARCHAR(70) NOT NULL DEFAULT '',
  `purpose`           VARCHAR(24) NOT NULL DEFAULT '',
  `created_at`        DATETIME(3) NULL,
  `updated_at`        DATETIME(3) NULL,
  `finished_at`       DATETIME(3) NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_energy_request` (`request_id`),
  KEY `idx_energy_provider` (`provider`),
  KEY `idx_energy_order_id` (`provider_order_id`),
  KEY `idx_energy_receiver` (`receive_address`),
  KEY `idx_energy_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `topup_record` (
  `id`                  BIGINT       NOT NULL AUTO_INCREMENT,
  `provider`            VARCHAR(32)  NOT NULL DEFAULT '',
  `request_id`          VARCHAR(64)  NOT NULL,
  `from_address`        VARCHAR(64)  NOT NULL DEFAULT '',
  `to_address`          VARCHAR(64)  NOT NULL DEFAULT '',
  `amount_trx`          DOUBLE       NOT NULL DEFAULT 0,
  `trigger_balance_trx` DOUBLE       NOT NULL DEFAULT 0,
  `txid`                VARCHAR(70)  NOT NULL DEFAULT '',
  `status`              VARCHAR(16)  NOT NULL DEFAULT 'created',
  `operator`            VARCHAR(32)  NOT NULL DEFAULT 'auto',
  `fail_reason`         VARCHAR(255) NOT NULL DEFAULT '',
  `created_at`          DATETIME(3)  NULL,
  `updated_at`          DATETIME(3)  NULL,
  `confirmed_at`        DATETIME(3)  NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_topup_request` (`request_id`),
  KEY `idx_topup_provider` (`provider`),
  KEY `idx_topup_status` (`status`),
  KEY `idx_topup_txid` (`txid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `chain_cursor` (
  `name`         VARCHAR(32) NOT NULL,
  `block_number` BIGINT      NOT NULL DEFAULT 0,
  `block_hash`   VARCHAR(70) NOT NULL DEFAULT '',
  `updated_at`   DATETIME(3) NULL,
  PRIMARY KEY (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `block_snapshot` (
  `block_number` BIGINT      NOT NULL,
  `block_hash`   VARCHAR(70) NOT NULL DEFAULT '',
  `parent_hash`  VARCHAR(70) NOT NULL DEFAULT '',
  `block_time`   DATETIME(3) NULL,
  `created_at`   DATETIME(3) NULL,
  PRIMARY KEY (`block_number`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `notify_outbox` (
  `id`          BIGINT       NOT NULL AUTO_INCREMENT,
  `event_id`    VARCHAR(96)  NOT NULL,
  `event_type`  VARCHAR(32)  NOT NULL DEFAULT '',
  `payload`     TEXT         NULL,
  `status`      VARCHAR(16)  NOT NULL DEFAULT 'pending',
  `retry_count` INT          NOT NULL DEFAULT 0,
  `next_retry`  DATETIME(3)  NULL,
  `last_error`  VARCHAR(255) NOT NULL DEFAULT '',
  `created_at`  DATETIME(3)  NULL,
  `updated_at`  DATETIME(3)  NULL,
  `sent_at`     DATETIME(3)  NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_outbox_event` (`event_id`),
  KEY `idx_outbox_type` (`event_type`),
  KEY `idx_outbox_status` (`status`),
  KEY `idx_outbox_next_retry` (`next_retry`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS `sign_audit` (
  `id`         BIGINT       NOT NULL AUTO_INCREMENT,
  `purpose`    VARCHAR(24)  NOT NULL DEFAULT '',
  `path`       VARCHAR(64)  NOT NULL DEFAULT '',
  `address`    VARCHAR(64)  NOT NULL DEFAULT '',
  `txid`       VARCHAR(70)  NOT NULL DEFAULT '',
  `caller`     VARCHAR(64)  NOT NULL DEFAULT '',
  `allowed`    TINYINT(1)   NOT NULL DEFAULT 0,
  `reason`     VARCHAR(255) NOT NULL DEFAULT '',
  `created_at` DATETIME(3)  NULL,
  PRIMARY KEY (`id`),
  KEY `idx_sign_purpose` (`purpose`),
  KEY `idx_sign_address` (`address`),
  KEY `idx_sign_txid` (`txid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
