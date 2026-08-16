-- ----------------------------
-- Every record of a transfer keeps the amount a second time, in the token's
-- own unit and as text: 13 USDT is '13', 12.345678 USDT is '12.345678', and a
-- TRX payment to an energy rental platform is stored the same way. The raw
-- minimum-unit / float columns stay authoritative; this one exists so a report
-- or a query never has to count zeros.
-- Additive, so it can be applied while the services are running.
-- ----------------------------
ALTER TABLE `withdraw_record`
  ADD COLUMN `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `amount_units`;

ALTER TABLE `deposit_record`
  ADD COLUMN `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `amount_units`;

ALTER TABLE `sweep_record`
  ADD COLUMN `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `amount_units`,
  ADD COLUMN `decimals` int NULL DEFAULT NULL AFTER `amount`;

ALTER TABLE `energy_rent_order`
  ADD COLUMN `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `cost_trx`;

ALTER TABLE `topup_record`
  ADD COLUMN `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '' AFTER `amount_trx`;
