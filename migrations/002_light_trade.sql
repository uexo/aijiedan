-- 002_light_trade.sql — light 模式增量迁移：新增 8 张表 + 存量 4 张表加列
-- 依赖：必须先执行 001_init（即 migrations/schema.sql，22 张基础表），本文件只做增量，不含建库与建基础表
-- 幂等：8 张 CREATE TABLE 均带 IF NOT EXISTS 可重复执行；4 条 ALTER 不可重复执行，重复会报 1060（属预期，见文件末尾）
-- 执行：mysql -h<host> -P<port> -u<user> -p -D<db> < migrations/002_light_trade.sql（库名由连接串指定，文件内不写 CREATE DATABASE / USE）
-- 日期：2026-10-20

-- ============================================================================
-- 一、新增表（8 张，顺序按设计文档 2.1–2.8）
-- 全表无任何可空列：规避 Go 侧 sql.Scan 遇到 NULL 报 converting NULL to string 的问题
-- ============================================================================

-- 2.1 供给方档案：没有档案的人不进召回池；completeness 落库而非实时计算，用于召回排序
CREATE TABLE IF NOT EXISTS `provider_profile` (
  `user_id`      VARCHAR(32) NOT NULL,
  `headline`     VARCHAR(60) NOT NULL DEFAULT '',
  `bio`          VARCHAR(200) NOT NULL DEFAULT '',
  `skills`       JSON        NOT NULL DEFAULT (JSON_ARRAY()),
  `portfolio`    JSON        NOT NULL DEFAULT (JSON_ARRAY()),
  `availability` TINYINT     NOT NULL DEFAULT 1,
  `completeness` TINYINT     NOT NULL DEFAULT 0,
  `updated_at`   BIGINT      NOT NULL DEFAULT 0,
  PRIMARY KEY (`user_id`),
  KEY `idx_avail` (`availability`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2.2 背书：产品的核心信用资产。一笔订单一人一条（uk_order_author 在库层挡重复提交）
-- source 区分真人与历史回填：live=真实交易产生，legacy=由旧 review 转换（权重降半，可整体重来）
CREATE TABLE IF NOT EXISTS `endorsement` (
  `id`         VARCHAR(32)   NOT NULL,
  `order_id`   VARCHAR(32)   NOT NULL,
  `task_id`    VARCHAR(32)   NOT NULL DEFAULT '',
  `author_id`  VARCHAR(32)   NOT NULL,
  `target_id`  VARCHAR(32)   NOT NULL,
  `relation`   VARCHAR(24)   NOT NULL DEFAULT 'client',
  `body`       VARCHAR(1000) NOT NULL,
  `answers`    JSON          NOT NULL DEFAULT (JSON_OBJECT()),
  `char_count` INT           NOT NULL DEFAULT 0,
  `status`     VARCHAR(16)   NOT NULL DEFAULT 'active',
  `weight`     INT           NOT NULL DEFAULT 100,
  `source`     VARCHAR(16)   NOT NULL DEFAULT 'live',
  `created_at` BIGINT        NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_order_author` (`order_id`, `author_id`),
  KEY `idx_target` (`target_id`, `status`, `created_at`),
  KEY `idx_author_window` (`author_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2.3 背书追加：原文不可编辑，只能追加；仅原作者可写，展示在原文下方并带独立时间戳
CREATE TABLE IF NOT EXISTS `endorsement_append` (
  `id`             VARCHAR(32) NOT NULL,
  `endorsement_id` VARCHAR(32) NOT NULL,
  `author_id`      VARCHAR(32) NOT NULL,
  `body`           VARCHAR(500) NOT NULL,
  `created_at`     BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_end` (`endorsement_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2.4 推荐理由：一条推荐位三条理由，每条必须挂可验证锚点；factor + factor_score 支持非功能需求"可复算"
-- anchor_ref 给到 255：portfolio 类锚点存的是完整 URL，64 在 strict 模式下会 1406 截断报错
CREATE TABLE IF NOT EXISTS `match_reason` (
  `id`           VARCHAR(32)  NOT NULL,
  `match_id`     VARCHAR(32)  NOT NULL,
  `rank_no`      TINYINT      NOT NULL,
  `text`         VARCHAR(200) NOT NULL,
  `anchor_type`  VARCHAR(16)  NOT NULL,
  `anchor_ref`   VARCHAR(255) NOT NULL,
  `anchor_label` VARCHAR(120) NOT NULL,
  `factor`       VARCHAR(16)  NOT NULL,
  `factor_score` INT          NOT NULL,
  `created_at`   BIGINT       NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_match` (`match_id`, `rank_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2.5 信用事件：信用分从"一个数字"变成"一串可解释事件"，user.credit_score 仅作快照由事件驱动写入
-- uk_user_kind_ref 是防重护栏：同一订单的 order_completed 只允许消费一次，否则重试会把信用分重复加减
CREATE TABLE IF NOT EXISTS `credit_event` (
  `id`          VARCHAR(32) NOT NULL,
  `user_id`     VARCHAR(32) NOT NULL,
  `kind`        VARCHAR(24) NOT NULL,
  `ref_id`      VARCHAR(32) NOT NULL,
  `delta`       INT         NOT NULL,
  `score_after` INT         NOT NULL,
  `created_at`  BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_kind_ref` (`user_id`, `kind`, `ref_id`),
  KEY `idx_user` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2.6 反刷标记：标记不等于处罚，confirmed 才把背书置 flagged/weight=0，dismissed 留痕
CREATE TABLE IF NOT EXISTS `abuse_flag` (
  `id`         VARCHAR(32)  NOT NULL,
  `kind`       VARCHAR(32)  NOT NULL,
  `subject_id` VARCHAR(32)  NOT NULL,
  `evidence`   JSON         NOT NULL DEFAULT (JSON_OBJECT()),
  `status`     VARCHAR(16)  NOT NULL DEFAULT 'pending',
  `handled_by` VARCHAR(32)  NOT NULL DEFAULT '',
  `note`       VARCHAR(200) NOT NULL DEFAULT '',
  `created_at` BIGINT       NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_status` (`status`, `created_at`),
  KEY `idx_subject` (`kind`, `subject_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2.7 审计日志：承接改档案/改状态/运营操作/匿名解锁等敏感操作，trace_id 与响应体、日志对得上
CREATE TABLE IF NOT EXISTS `audit_log` (
  `id`          BIGINT      NOT NULL AUTO_INCREMENT,
  `actor_id`    VARCHAR(32) NOT NULL DEFAULT '',
  `action`      VARCHAR(48) NOT NULL,
  `target_type` VARCHAR(24) NOT NULL,
  `target_id`   VARCHAR(32) NOT NULL,
  `detail`      JSON        NOT NULL DEFAULT (JSON_OBJECT()),
  `trace_id`    VARCHAR(32) NOT NULL DEFAULT '',
  `ip`          VARCHAR(45) NOT NULL DEFAULT '',
  `created_at`  BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_target` (`target_type`, `target_id`, `created_at`),
  KEY `idx_actor` (`actor_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2.8 幂等键：发单/下单/验收/写背书四个写接口的弱网重复提交防护，24 小时内同 key 回放同一响应
CREATE TABLE IF NOT EXISTS `idempotency_key` (
  `key_hash`      CHAR(64)   NOT NULL,
  `user_id`       VARCHAR(32) NOT NULL,
  `endpoint`      VARCHAR(64) NOT NULL,
  `response_code` INT         NOT NULL,
  `response_body` MEDIUMTEXT  NOT NULL DEFAULT (''),
  `created_at`    BIGINT      NOT NULL,
  PRIMARY KEY (`key_hash`),
  KEY `idx_created` (`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ============================================================================
-- 二、存量表变更（4 条 ALTER）
-- 目标表 user / task / order / match_record 均由 001_init 创建，必须先跑完 001
-- 本段放在全部 CREATE TABLE 之后：迁移器按文件顺序执行时，避免 1146 table doesn't exist
-- ============================================================================

-- 业务目的：公开信用页要 /p/{slug}，slug 必须是短号而非内部用户 ID（id 会暴露编号规律与总量）
-- 分三步：先加列 → 回填（用 id 小写兜底，存量行必唯一）→ 再加唯一索引。
-- 不能一步到位：所有存量行 slug 都是 ''，直接建 UNIQUE 会立刻冲突。
ALTER TABLE `user`
  ADD COLUMN `slug`      VARCHAR(8) NOT NULL DEFAULT '',
  ADD COLUMN `onboarded` TINYINT    NOT NULL DEFAULT 0;

UPDATE `user` SET `slug` = LOWER(`id`) WHERE `slug` = '';

ALTER TABLE `user`
  ADD UNIQUE KEY `uk_slug` (`slug`);

-- 业务目的：补齐 PRD 的两项发布必填（交付什么/怎么算完成）、区间预算与发布时刻，并记录结构化来源是规则还是模型
ALTER TABLE `task`
  ADD COLUMN `deliverable`   VARCHAR(300) NOT NULL DEFAULT '',
  ADD COLUMN `acceptance`    VARCHAR(300) NOT NULL DEFAULT '',
  ADD COLUMN `budget_band`   VARCHAR(24)  NOT NULL DEFAULT 'undecided',
  ADD COLUMN `published_at`  BIGINT       NOT NULL DEFAULT 0,
  ADD COLUMN `struct_source` VARCHAR(8)   NOT NULL DEFAULT 'rule';

-- 业务目的：light 模式四态需要"供给方确认接单时刻"与"匿名解锁标记"，并用 mode 区分托管/轻交易两条链路
ALTER TABLE `order`
  ADD COLUMN `confirmed_at`  BIGINT      NOT NULL DEFAULT 0,
  ADD COLUMN `anon_released` TINYINT     NOT NULL DEFAULT 0,
  ADD COLUMN `mode`          VARCHAR(8)  NOT NULL DEFAULT 'light';

-- 业务目的：区分 v1 撮合与 v2 匹配的两种记录，旧数据统一回落 escrow，避免统计混算
ALTER TABLE `match_record`
  ADD COLUMN `mode` VARCHAR(8) NOT NULL DEFAULT 'escrow';

-- ============================================================================
-- 注意：重复执行本文件的 ALTER 部分会报 1060 duplicate column name，属预期行为。
-- MySQL 8 不支持 ADD COLUMN IF NOT EXISTS，幂等由迁移器版本表（如 schema_migrations）保证只执行一次。
-- ============================================================================
