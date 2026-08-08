-- Merchants. Every user belongs to exactly one merchant: a deposit address is
-- allocated per (merchant_id, uid) and confirmed deposits are notified to the
-- merchant's own callback URL, signed with the merchant's own sha256 secret.
--   * merchant.merchant_id  one row per merchant
--   * wallet.account        merchant_id + "_" + uid, one deposit address each

CREATE TABLE IF NOT EXISTS `merchant` (
  `id`           BIGINT       NOT NULL AUTO_INCREMENT,
  `merchant_id`  VARCHAR(64)  NOT NULL,
  `name`         VARCHAR(64)  NOT NULL DEFAULT '',
  `callback_url` VARCHAR(255) NOT NULL DEFAULT '',
  -- Signing key for inbound parameters and outbound callbacks. Never returned
  -- by the API.
  `secret`       VARCHAR(128) NOT NULL DEFAULT '',
  `status`       TINYINT      NOT NULL DEFAULT 1,
  `created_at`   DATETIME(3)  NULL,
  `updated_at`   DATETIME(3)  NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uq_merchant_id` (`merchant_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- account stays NULL for platform owned wallets (hot, finance, gas), which have
-- no merchant; the unique key therefore only constrains user deposit accounts.
ALTER TABLE `wallet`
  ADD COLUMN `merchant_id` VARCHAR(64) NOT NULL DEFAULT '' AFTER `id`,
  ADD COLUMN `account`     VARCHAR(128) NULL DEFAULT NULL AFTER `merchant_id`,
  ADD UNIQUE KEY `uq_wallet_account` (`account`),
  ADD KEY `idx_wallet_merchant` (`merchant_id`);

ALTER TABLE `deposit_record`
  ADD COLUMN `merchant_id` VARCHAR(64) NOT NULL DEFAULT '' AFTER `id`,
  ADD KEY `idx_deposit_merchant` (`merchant_id`);

ALTER TABLE `notify_outbox`
  ADD COLUMN `merchant_id` VARCHAR(64) NOT NULL DEFAULT '' AFTER `event_id`,
  ADD KEY `idx_outbox_merchant` (`merchant_id`);
