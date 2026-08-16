-- ----------------------------
-- Every record of a transfer keeps the block its transaction was included in,
-- so an order can be traced to a height without a node lookup. deposit_record
-- already has block_number, written by the scanner.
-- The column stays NULL/0 until the receipt is read; energy_rent_order stores
-- the block of the provider's delegation transaction (a burn order has none).
-- Additive, so it can be applied while the services are running.
-- ----------------------------
ALTER TABLE `withdraw_record`
  ADD COLUMN `block_number` bigint NULL DEFAULT NULL AFTER `txid`,
  ADD INDEX `idx_withdraw_record_block_number`(`block_number` ASC) USING BTREE;

ALTER TABLE `sweep_record`
  ADD COLUMN `block_number` bigint NULL DEFAULT NULL AFTER `txid`,
  ADD INDEX `idx_sweep_record_block_number`(`block_number` ASC) USING BTREE;

ALTER TABLE `energy_rent_order`
  ADD COLUMN `block_number` bigint NULL DEFAULT NULL AFTER `delegate_txid`,
  ADD INDEX `idx_energy_rent_order_block_number`(`block_number` ASC) USING BTREE;

ALTER TABLE `topup_record`
  ADD COLUMN `block_number` bigint NULL DEFAULT NULL AFTER `txid`,
  ADD INDEX `idx_topup_record_block_number`(`block_number` ASC) USING BTREE;
