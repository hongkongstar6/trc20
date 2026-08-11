-- Failure classification, sweep retry accounting and the energy delegation
-- baseline. Every statement is additive, so it can be applied while the
-- services are running.

ALTER TABLE `withdraw_record`
  ADD COLUMN `fail_code` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL AFTER `fail_reason`,
  ADD INDEX `idx_withdraw_record_fail_code`(`fail_code` ASC) USING BTREE;

ALTER TABLE `sweep_record`
  ADD COLUMN `fail_code` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL AFTER `fail_reason`,
  -- Consecutive OUT_OF_ENERGY failures of the address before this attempt.
  ADD COLUMN `retry_count` int NULL DEFAULT 0 AFTER `fail_code`,
  -- Highest deposit_record id covered by this sweep; deposits confirming while
  -- the sweep is in flight keep their unswept flag.
  ADD COLUMN `deposit_max_id` bigint NULL DEFAULT 0 AFTER `retry_count`,
  ADD INDEX `idx_sweep_record_fail_code`(`fail_code` ASC) USING BTREE;

ALTER TABLE `energy_rent_order`
  -- Energy the receiving address could already spend when the order was placed.
  -- The delegation is confirmed against this baseline, not the absolute balance.
  ADD COLUMN `baseline_energy` bigint NULL DEFAULT 0 AFTER `delegated_energy`;
