-- Per-order withdrawal callback URL. The business system submits notify_url
-- with the order and the final outcome (confirmed after 19 blocks, failed or
-- rejected) is posted to that URL. Additive, so it can be applied while the
-- services are running.

ALTER TABLE `withdraw_record`
  ADD COLUMN `notify_url` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL AFTER `to_address`;

ALTER TABLE `notify_outbox`
  -- Overrides the merchant callback URL for this event only.
  ADD COLUMN `notify_url` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL AFTER `account`;
