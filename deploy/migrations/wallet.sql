/*
 Navicat Premium Dump SQL

 Source Server         : Docker MySQL
 Source Server Type    : MySQL
 Source Server Version : 80046 (8.0.46)
 Source Host           : localhost:3306
 Source Schema         : wallet

 Target Server Type    : MySQL
 Target Server Version : 80046 (8.0.46)
 File Encoding         : 65001

 Date: 17/08/2026 01:11:35
*/

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;

-- ----------------------------
-- Table structure for address_blacklist
-- ----------------------------
DROP TABLE IF EXISTS `address_blacklist`;
CREATE TABLE `address_blacklist`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `add_time` datetime(3) NULL DEFAULT NULL,
  `account` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '',
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `idx_address_blacklist_address`(`address` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 1 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for block_snapshot
-- ----------------------------
DROP TABLE IF EXISTS `block_snapshot`;
CREATE TABLE `block_snapshot`  (
  `block_number` bigint NOT NULL,
  `block_hash` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `parent_hash` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `block_time` datetime(3) NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`block_number`) USING BTREE
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for chain_cursor
-- ----------------------------
DROP TABLE IF EXISTS `chain_cursor`;
CREATE TABLE `chain_cursor`  (
  `name` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL,
  `block_number` bigint NULL DEFAULT NULL,
  `block_hash` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`name`) USING BTREE
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for deposit_record
-- ----------------------------
DROP TABLE IF EXISTS `deposit_record`;
CREATE TABLE `deposit_record`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `account` varchar(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `uid` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL,
  `chain` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `symbol` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `contract` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `txid` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `event_index` int NULL DEFAULT NULL,
  `block_number` bigint NULL DEFAULT NULL,
  `block_hash` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `from_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `to_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount_units` decimal(38, 0) NULL DEFAULT NULL,
  `decimals` int NULL DEFAULT NULL,
  `confirmations` bigint NULL DEFAULT NULL,
  `status` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `internal` tinyint(1) NULL DEFAULT NULL,
  `swept` tinyint(1) NULL DEFAULT NULL,
  `block_time` datetime(3) NULL DEFAULT NULL,
  `confirmed_at` datetime(3) NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  `merchant_id` varchar(30) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `trade_no` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,

  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `uq_tx_event`(`txid` ASC, `event_index` ASC) USING BTREE,
  UNIQUE INDEX `idx_deposit_record_trade_no`(`trade_no` ASC) USING BTREE,
  INDEX `idx_deposit_uid`(`account` ASC) USING BTREE,
  INDEX `idx_deposit_to`(`to_address` ASC) USING BTREE,
  INDEX `idx_deposit_status`(`status` ASC) USING BTREE,
  INDEX `idx_deposit_swept`(`swept` ASC) USING BTREE,
  INDEX `idx_deposit_block`(`block_number` ASC) USING BTREE,
  INDEX `idx_deposit_record_uid`(`account` ASC) USING BTREE,
  INDEX `idx_deposit_record_block_number`(`block_number` ASC) USING BTREE,
  INDEX `idx_deposit_record_to_address`(`to_address` ASC) USING BTREE,
  INDEX `idx_deposit_record_status`(`status` ASC) USING BTREE,
  INDEX `idx_deposit_record_swept`(`swept` ASC) USING BTREE,
  INDEX `idx_deposit_record_merchant_id`(`merchant_id` ASC) USING BTREE,
  INDEX `idx_deposit_record_account`(`account` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 22 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for energy_rent_order
-- ----------------------------
DROP TABLE IF EXISTS `energy_rent_order`;
CREATE TABLE `energy_rent_order`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `provider` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `request_id` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `provider_order_id` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `receive_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `resource_type` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `requested_energy` bigint NULL DEFAULT NULL,
  `delegated_energy` bigint NULL DEFAULT NULL,
  `period` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `cost_trx` double NULL DEFAULT NULL,
  `status` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `provider_status` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `delegate_txid` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `purpose` varchar(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  `finished_at` datetime(3) NULL DEFAULT NULL,
  `baseline_energy` bigint NULL DEFAULT NULL,
  `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `block_number` bigint NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `idx_energy_rent_order_request_id`(`request_id` ASC) USING BTREE,
  INDEX `idx_energy_provider`(`provider` ASC) USING BTREE,
  INDEX `idx_energy_order_id`(`provider_order_id` ASC) USING BTREE,
  INDEX `idx_energy_receiver`(`receive_address` ASC) USING BTREE,
  INDEX `idx_energy_status`(`status` ASC) USING BTREE,
  INDEX `idx_energy_rent_order_provider`(`provider` ASC) USING BTREE,
  INDEX `idx_energy_rent_order_provider_order_id`(`provider_order_id` ASC) USING BTREE,
  INDEX `idx_energy_rent_order_receive_address`(`receive_address` ASC) USING BTREE,
  INDEX `idx_energy_rent_order_status`(`status` ASC) USING BTREE,
  INDEX `idx_energy_rent_order_block_number`(`block_number` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 534 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for merchant
-- ----------------------------
DROP TABLE IF EXISTS `merchant`;
CREATE TABLE `merchant`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `merchant_id` varchar(30) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `name` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `callback_url` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `symbol` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `chain` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `secret` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `status` tinyint NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `idx_merchant_merchant_id`(`merchant_id` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 3 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for notify_outbox
-- ----------------------------
DROP TABLE IF EXISTS `notify_outbox`;
CREATE TABLE `notify_outbox`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `event_id` varchar(96) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `event_type` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `payload` text CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL,
  `status` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `retry_count` int NULL DEFAULT NULL,
  `next_retry` datetime(3) NULL DEFAULT NULL,
  `last_error` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  `sent_at` datetime(3) NULL DEFAULT NULL,
  `merchant_id` varchar(30) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `account` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `notify_url` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `idx_notify_outbox_event_id`(`event_id` ASC) USING BTREE,
  INDEX `idx_outbox_type`(`event_type` ASC) USING BTREE,
  INDEX `idx_outbox_status`(`status` ASC) USING BTREE,
  INDEX `idx_outbox_next_retry`(`next_retry` ASC) USING BTREE,
  INDEX `idx_notify_outbox_event_type`(`event_type` ASC) USING BTREE,
  INDEX `idx_notify_outbox_status`(`status` ASC) USING BTREE,
  INDEX `idx_notify_outbox_next_retry`(`next_retry` ASC) USING BTREE,
  INDEX `idx_notify_outbox_merchant_id`(`merchant_id` ASC) USING BTREE,
  INDEX `idx_notify_outbox_account`(`account` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 29 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for sign_audit
-- ----------------------------
DROP TABLE IF EXISTS `sign_audit`;
CREATE TABLE `sign_audit`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `purpose` varchar(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `path` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `txid` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `caller` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `allowed` tinyint(1) NULL DEFAULT NULL,
  `reason` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  INDEX `idx_sign_purpose`(`purpose` ASC) USING BTREE,
  INDEX `idx_sign_address`(`address` ASC) USING BTREE,
  INDEX `idx_sign_txid`(`txid` ASC) USING BTREE,
  INDEX `idx_sign_audit_purpose`(`purpose` ASC) USING BTREE,
  INDEX `idx_sign_audit_address`(`address` ASC) USING BTREE,
  INDEX `idx_sign_audit_tx_id`(`txid` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 18 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for sweep_record
-- ----------------------------
DROP TABLE IF EXISTS `sweep_record`;
CREATE TABLE `sweep_record`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `from_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `to_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `symbol` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `contract` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount_units` decimal(38, 0) NULL DEFAULT NULL,
  `status` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `txid` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `signed_raw` text CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL,
  `fee_mode` varchar(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `energy_order` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `cost_trx` double NULL DEFAULT NULL,
  `energy_used` bigint NULL DEFAULT NULL,
  `fail_reason` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `broadcast_at` datetime(3) NULL DEFAULT NULL,
  `confirmed_at` datetime(3) NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  `expired_at` datetime(3) NULL DEFAULT NULL,
  `fail_code` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `retry_count` int NULL DEFAULT NULL,
  `deposit_max_id` bigint NULL DEFAULT NULL,  
  `decimals` int NULL DEFAULT NULL,
  `block_number` bigint NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  INDEX `idx_sweep_from`(`from_address` ASC) USING BTREE,
  INDEX `idx_sweep_status`(`status` ASC) USING BTREE,
  INDEX `idx_sweep_txid`(`txid` ASC) USING BTREE,
  INDEX `idx_sweep_record_from_address`(`from_address` ASC) USING BTREE,
  INDEX `idx_sweep_record_status`(`status` ASC) USING BTREE,
  INDEX `idx_sweep_record_tx_id`(`txid` ASC) USING BTREE,
  INDEX `idx_sweep_record_fail_code`(`fail_code` ASC) USING BTREE,
  INDEX `idx_sweep_record_block_number`(`block_number` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 12 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for sweep_skip
-- ----------------------------
DROP TABLE IF EXISTS `sweep_skip`;
CREATE TABLE `sweep_skip`  (
  `address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL,
  `contract` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL DEFAULT '',
  `skip_count` bigint NULL DEFAULT NULL,
  `last_skip_at` datetime(3) NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`address`, `contract`) USING BTREE
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for topup_record
-- ----------------------------
DROP TABLE IF EXISTS `topup_record`;
CREATE TABLE `topup_record`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `provider` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `request_id` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `from_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `to_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount_trx` double NULL DEFAULT NULL,
  `trigger_balance_trx` double NULL DEFAULT NULL,
  `txid` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `status` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `operator` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `fail_reason` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  `confirmed_at` datetime(3) NULL DEFAULT NULL,  
  `block_number` bigint NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `idx_topup_record_request_id`(`request_id` ASC) USING BTREE,
  INDEX `idx_topup_provider`(`provider` ASC) USING BTREE,
  INDEX `idx_topup_status`(`status` ASC) USING BTREE,
  INDEX `idx_topup_txid`(`txid` ASC) USING BTREE,
  INDEX `idx_topup_record_provider`(`provider` ASC) USING BTREE,
  INDEX `idx_topup_record_tx_id`(`txid` ASC) USING BTREE,
  INDEX `idx_topup_record_status`(`status` ASC) USING BTREE,
  INDEX `idx_topup_record_block_number`(`block_number` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 1 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for user_wallet
-- ----------------------------
DROP TABLE IF EXISTS `user_wallet`;
CREATE TABLE `user_wallet`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `uid` varchar(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `merchant_id` varchar(30) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `account` varchar(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `chain` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `addr_index` bigint NULL DEFAULT NULL,
  `chain_idx` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `derive_path` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `purpose` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `status` tinyint NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `uq_chain_index`(`chain_idx` ASC, `addr_index` ASC) USING BTREE,
  UNIQUE INDEX `idx_user_wallet_account`(`account` ASC) USING BTREE,
  UNIQUE INDEX `idx_user_wallet_address`(`address` ASC) USING BTREE,
  INDEX `idx_wallet_uid`(`uid` ASC) USING BTREE,
  INDEX `idx_wallet_purpose`(`purpose` ASC) USING BTREE,
  INDEX `idx_wallet_merchant_id`(`merchant_id` ASC) USING BTREE,
  INDEX `idx_user_wallet_merchant_id`(`merchant_id` ASC) USING BTREE,
  INDEX `idx_user_wallet_uid`(`uid` ASC) USING BTREE,
  INDEX `idx_user_wallet_purpose`(`purpose` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 6 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for wallet_index_allocator
-- ----------------------------
DROP TABLE IF EXISTS `wallet_index_allocator`;
CREATE TABLE `wallet_index_allocator`  (
  `chain` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NOT NULL,
  `next_index` bigint NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  PRIMARY KEY (`chain`) USING BTREE
) ENGINE = InnoDB CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Table structure for withdraw_record
-- ----------------------------
DROP TABLE IF EXISTS `withdraw_record`;
CREATE TABLE `withdraw_record`  (
  `id` bigint NOT NULL AUTO_INCREMENT,
  `order_no` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `ext_param` varchar(191) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `chain` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `symbol` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `contract` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `from_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `to_address` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `notify_url` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `amount_units` decimal(38, 0) NULL DEFAULT NULL,
  `decimals` int NULL DEFAULT NULL,
  `status` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `fail_reason` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `txid` varchar(70) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `signed_raw` text CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL,
  `expired_at` datetime(3) NULL DEFAULT NULL,
  `energy_used` bigint NULL DEFAULT NULL,
  `fee_sun` bigint NULL DEFAULT NULL,
  `broadcast_at` datetime(3) NULL DEFAULT NULL,
  `confirmed_at` datetime(3) NULL DEFAULT NULL,
  `created_at` datetime(3) NULL DEFAULT NULL,
  `updated_at` datetime(3) NULL DEFAULT NULL,
  `merchant_id` varchar(30) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `fail_code` varchar(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `trade_no` varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci NULL DEFAULT NULL,
  `halt_count` bigint NULL DEFAULT 0,  
  `block_number` bigint NULL DEFAULT NULL,
  PRIMARY KEY (`id`) USING BTREE,
  UNIQUE INDEX `idx_withdraw_record_order_no`(`order_no` ASC) USING BTREE,
  UNIQUE INDEX `idx_withdraw_record_trade_no`(`trade_no` ASC) USING BTREE,
  INDEX `idx_withdraw_uid`(`ext_param` ASC) USING BTREE,
  INDEX `idx_withdraw_to`(`to_address` ASC) USING BTREE,
  INDEX `idx_withdraw_status`(`status` ASC) USING BTREE,
  INDEX `idx_withdraw_txid`(`txid` ASC) USING BTREE,
  INDEX `idx_withdraw_record_uid`(`ext_param` ASC) USING BTREE,
  INDEX `idx_withdraw_record_to_address`(`to_address` ASC) USING BTREE,
  INDEX `idx_withdraw_record_status`(`status` ASC) USING BTREE,
  INDEX `idx_withdraw_record_tx_id`(`txid` ASC) USING BTREE,
  INDEX `idx_withdraw_record_account`(`ext_param` ASC) USING BTREE,
  INDEX `idx_withdraw_record_fail_code`(`fail_code` ASC) USING BTREE,
  INDEX `idx_withdraw_record_ext_param`(`ext_param` ASC) USING BTREE,
  INDEX `idx_withdraw_record_block_number`(`block_number` ASC) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 14 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

SET FOREIGN_KEY_CHECKS = 1;
