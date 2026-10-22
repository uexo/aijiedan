-- Zanbuddy M0 MySQL DDL (生产切换用)
-- 说明: M0 骨架默认使用内存存储(store.Memory)零依赖启动。
-- 接入真实 MySQL 时: 1) 创建库  2) 执行本文件  3) 在 internal/store 切换为 MySQL 实现(接口已抽象)。
-- 所有金额字段使用 BIGINT(分), 主键用 BIGINT 雪花ID或 VARCHAR(32) 业务ID, 软删字段 deleted_at。

CREATE DATABASE IF NOT EXISTS zanbuddy DEFAULT CHARACTER SET utf8mb4;
USE zanbuddy;

CREATE TABLE `user` (
  `id`           VARCHAR(32)    NOT NULL,
  `account`      VARCHAR(64) NULL UNIQUE,
  `password_hash` VARCHAR(255) NULL,
  `real_name`    VARCHAR(64) NULL,
  `phone`        VARCHAR(32) NULL,
  `name`         VARCHAR(64) NOT NULL,
  `role`         VARCHAR(16) NOT NULL,
  `kyc_verified` TINYINT(1)  NOT NULL DEFAULT 0,
  `skills`       JSON        NULL,
  `credit_score` INT         NOT NULL DEFAULT 50,
  `tax_bound`    TINYINT(1)  NOT NULL DEFAULT 0,
  `created_at`   BIGINT      NOT NULL,
  `deleted_at`   BIGINT      NULL,
  PRIMARY KEY (`id`),
  KEY `idx_role` (`role`),
  KEY `idx_account` (`account`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `task` (
  `id`           VARCHAR(32)    NOT NULL,
  `requester_id` VARCHAR(32)    NOT NULL,
  `org_id`       VARCHAR(32) NULL,
  `title`        VARCHAR(128) NOT NULL,
  `category`     VARCHAR(32) NOT NULL,
  `budget_cents` BIGINT      NOT NULL,
  `spec`         TEXT        NULL,
  `deliverables`   VARCHAR(255) NULL,
  `required_format` VARCHAR(128) NULL,
  `deadline_at`   BIGINT      NULL,
  `style_ref`     VARCHAR(255) NULL,
  `samples`       VARCHAR(512) NULL,
  `clarifications` TEXT       NULL,
  `status`       VARCHAR(16) NOT NULL,
  `created_at`   BIGINT      NOT NULL,
  `deleted_at`   BIGINT      NULL,
  PRIMARY KEY (`id`),
  KEY `idx_requester` (`requester_id`),
  KEY `idx_org` (`org_id`),
  KEY `idx_category_status` (`category`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- C1 AI 需求翻译 / 需求分析: task 表存储需求方侧翻译正文与接单方侧分析(JSON)
-- 注意: ALTER 必须置于 CREATE TABLE task 之后(否则 reset 报 1146 表不存在)
ALTER TABLE `task`
  ADD COLUMN `translation`      TEXT        NULL,
  ADD COLUMN `translation_mode` VARCHAR(16) NOT NULL DEFAULT '',
  ADD COLUMN `analysis_json`    JSON        NULL,
  ADD COLUMN `analysis_mode`    VARCHAR(16) NOT NULL DEFAULT '';

CREATE TABLE `match_record` (
  `id`           VARCHAR(32)    NOT NULL,
  `task_id`      VARCHAR(32)    NOT NULL,
  `provider_id`  VARCHAR(32)    NOT NULL,
  `score`        DOUBLE      NOT NULL,
  `reason`       VARCHAR(255) NULL,
  `status`       VARCHAR(16) NOT NULL,
  `created_at`   BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_task` (`task_id`),
  KEY `idx_provider` (`provider_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `order` (
  `id`           VARCHAR(32)    NOT NULL,
  `task_id`      VARCHAR(32)    NOT NULL,
  `requester_id` VARCHAR(32)    NOT NULL,
  `provider_id`  VARCHAR(32)    NOT NULL,
  `match_id`     VARCHAR(32)    NOT NULL,
  `org_id`       VARCHAR(32) NULL,
  `amount_cents` BIGINT      NOT NULL,
  `status`       VARCHAR(24) NOT NULL,
  `escrow_id`    VARCHAR(64) NULL,
  `settled`      TINYINT(1)  NOT NULL DEFAULT 0,
  `created_at`   BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_provider` (`provider_id`),
  KEY `idx_org` (`org_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- P1-4 订单完整化: order 表扩展(里程碑/取消/纠纷/退款/改稿上限)
-- 对存量库执行(MySQL 不支持 ADD COLUMN IF NOT EXISTS, 重复执行会报错, 请确认未执行过):
ALTER TABLE `order`
  ADD COLUMN `max_revisions`   INT          NOT NULL DEFAULT 3,
  ADD COLUMN `milestones`      JSON         NULL,
  ADD COLUMN `settlement_mode` VARCHAR(32)  NOT NULL DEFAULT 'lump',
  ADD COLUMN `refund_cents`    BIGINT       NOT NULL DEFAULT 0,
  ADD COLUMN `refund_pct`      INT          NOT NULL DEFAULT 0,
  ADD COLUMN `cancelled_by`    VARCHAR(64)  NULL,
  ADD COLUMN `cancel_reason`   VARCHAR(255) NULL,
  ADD COLUMN `cancelled_at`    BIGINT       NOT NULL DEFAULT 0,
  ADD COLUMN `dispute_reason`  VARCHAR(255) NULL,
  ADD COLUMN `disputed_by`     VARCHAR(64)  NULL,
  ADD COLUMN `disputed_at`     BIGINT       NOT NULL DEFAULT 0,
  ADD COLUMN `resolved_by`     VARCHAR(64)  NULL,
  ADD COLUMN `resolved_at`     BIGINT       NOT NULL DEFAULT 0,
  ADD COLUMN `resolved_note`   VARCHAR(255) NULL;

-- B方案(验收清单比对): order 表扩展验收留痕字段
ALTER TABLE `order`
  ADD COLUMN `acceptance_json`  JSON        NULL,
  ADD COLUMN `severe_mismatch`  TINYINT(1)  NOT NULL DEFAULT 0,
  ADD COLUMN `accepted_at`      BIGINT      NOT NULL DEFAULT 0;

-- B2 自动报价: order 表存储接单方预期报价(分)
ALTER TABLE `order`
  ADD COLUMN `provider_quote_cents` BIGINT NOT NULL DEFAULT 0;

-- B3 AI 客服应答: user 表存储是否开启 IM 离线自动回复
ALTER TABLE `user`
  ADD COLUMN `ai_auto_reply` TINYINT(1) NOT NULL DEFAULT 0;

-- B方案(验收清单比对): task 表存储结构化需求清单
ALTER TABLE `task`
  ADD COLUMN `items_json`  JSON  NULL;

CREATE TABLE `delivery` (
  `id`         VARCHAR(32)    NOT NULL,
  `order_id`   VARCHAR(32)    NOT NULL,
  `round`      INT         NOT NULL,
  `content`    TEXT        NULL,
  `status`     VARCHAR(16) NOT NULL,
  `created_at` BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_order` (`order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- B方案(验收清单比对): delivery 表存储逐需求项完成情况(须在 CREATE TABLE delivery 之后)
ALTER TABLE `delivery`
  ADD COLUMN `items_json`  JSON  NULL;

-- v2 验收: 打回说明落在被打回的那一轮交付上(须在 CREATE TABLE delivery 之后)
ALTER TABLE `delivery`
  ADD COLUMN `review_note`  TEXT   NULL,
  ADD COLUMN `reviewed_at`  BIGINT NOT NULL DEFAULT 0;

-- v2 交付: 交付物入口链接(平台不托管文件, 靠网盘/仓库/文档链接传递)
ALTER TABLE `delivery`
  ADD COLUMN `link` VARCHAR(500) NULL;

CREATE TABLE `compliance_state` (
  `item`   VARCHAR(16) NOT NULL,
  `on`     TINYINT(1)  NOT NULL,
  `note`   VARCHAR(255) NULL,
  PRIMARY KEY (`item`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `review` (
  `id`            VARCHAR(32)    NOT NULL,
  `order_id`      VARCHAR(32)    NOT NULL,
  `task_id`       VARCHAR(32)    NOT NULL,
  `reviewer_id`   VARCHAR(32)    NOT NULL,
  `reviewee_id`   VARCHAR(32)    NOT NULL,
  `reviewer_role` VARCHAR(16) NOT NULL,
  `rating`        TINYINT     NOT NULL,
  `comment`       VARCHAR(512) NULL,
  `created_at`    BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_order` (`order_id`),
  KEY `idx_reviewee` (`reviewee_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- P2-1 资金账户体系(钱包/流水/提现)。平台内记账模型, 真实资金走微信支付担保交易+分账。
CREATE TABLE `wallet` (
  `uid`           VARCHAR(32)    NOT NULL,
  `balance_cents` BIGINT      NOT NULL DEFAULT 0,
  `frozen_cents`  BIGINT      NOT NULL DEFAULT 0,
  `updated_at`    BIGINT      NOT NULL,
  PRIMARY KEY (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `ledger` (
  `id`             VARCHAR(32)    NOT NULL,
  `uid`            VARCHAR(32)    NOT NULL,
  `order_id`       VARCHAR(32) NULL,
  `type`           VARCHAR(16) NOT NULL,
  `amount_cents`   BIGINT      NOT NULL,
  `balance_after`  BIGINT      NOT NULL,
  `external_id`    VARCHAR(64) NULL,
  `note`           VARCHAR(255) NULL,
  `created_at`     BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_uid` (`uid`),
  KEY `idx_created` (`uid`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `withdrawal` (
  `id`            VARCHAR(32)    NOT NULL,
  `uid`           VARCHAR(32)    NOT NULL,
  `amount_cents`  BIGINT      NOT NULL,
  `status`        VARCHAR(16) NOT NULL,
  `created_at`    BIGINT      NOT NULL,
  `resolved_at`   BIGINT      NULL,
  `note`          VARCHAR(255) NULL,
  PRIMARY KEY (`id`),
  KEY `idx_uid` (`uid`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- P2-2 IM: 会话表(双方 + 订单关联 + 未读计数)
CREATE TABLE `conversation` (
  `id`           VARCHAR(48) NOT NULL,
  `a`            VARCHAR(32)    NOT NULL,
  `b`            VARCHAR(32)    NOT NULL,
  `order_id`     VARCHAR(32) NULL,
  `last_body`    VARCHAR(1024) NULL,
  `last_at`      BIGINT      NOT NULL DEFAULT 0,
  `a_unread`     INT         NOT NULL DEFAULT 0,
  `b_unread`     INT         NOT NULL DEFAULT 0,
  `updated_at`   BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_a` (`a`),
  KEY `idx_b` (`b`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- P2-2 IM: 聊天消息表(持久化, 重启不丢; status 由内容审核驱动)
CREATE TABLE `chat_message` (
  `id`          VARCHAR(32)    NOT NULL,
  `conv_id`     VARCHAR(48) NOT NULL,
  `from_id`     VARCHAR(32)    NOT NULL,
  `to_id`       VARCHAR(32)    NOT NULL,
  `body`        VARCHAR(4096) NOT NULL,
  `status`      VARCHAR(16) NOT NULL DEFAULT 'normal',
  `created_at`  BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_conv` (`conv_id`),
  KEY `idx_conv_time` (`conv_id`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- B3 AI 客服应答: chat_message 表记录消息是否由 AI 代发及 AI 类型(faq/llm/suggest)。
-- 必须置于 CREATE TABLE chat_message 之后(迁移顺序敏感)。
ALTER TABLE `chat_message`
  ADD COLUMN `is_ai`   TINYINT(1)  NOT NULL DEFAULT 0,
  ADD COLUMN `ai_type` VARCHAR(16) NOT NULL DEFAULT '';

-- P2-3 通知中心(站内信/邮件/短信; 平台→用户单向, 落库持久化)
CREATE TABLE `notification` (
  `id`         VARCHAR(32)     NOT NULL,
  `uid`        VARCHAR(32)     NOT NULL,
  `channel`    VARCHAR(16)  NOT NULL DEFAULT 'inapp',
  `title`      VARCHAR(255) NOT NULL,
  `body`       VARCHAR(1024) NOT NULL,
  `status`     VARCHAR(16)  NOT NULL DEFAULT 'unread',
  `created_at` BIGINT       NOT NULL,
  `read_at`    BIGINT       NULL,
  PRIMARY KEY (`id`),
  KEY `idx_uid` (`uid`),
  KEY `idx_uid_status` (`uid`,`status`),
  KEY `idx_uid_created` (`uid`,`created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- P3 企业版治理(设计阶段最小骨架: 组织/成员/角色 RBAC)。
CREATE TABLE `org` (
  `id`         VARCHAR(32)    NOT NULL,
  `name`       VARCHAR(128) NOT NULL,
  `owner_id`   VARCHAR(32)    NOT NULL,
  `plan`       VARCHAR(16) NOT NULL DEFAULT 'free',
  `status`     VARCHAR(16) NOT NULL DEFAULT 'active',
  `created_at` BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_owner` (`owner_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `org_member` (
  `org_id`    VARCHAR(32) NOT NULL,
  `user_id`   VARCHAR(32) NOT NULL,
  `role`      VARCHAR(16) NOT NULL DEFAULT 'member',
  `joined_at` BIGINT     NOT NULL,
  PRIMARY KEY (`org_id`,`user_id`),
  KEY `idx_user` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- P3 专项: 组织钱包 / 审批流 / 开放 API + Webhook
-- 组织钱包: 企业版统一结算/开票主体, 平台内记账(生产由微信支付担保交易+分账回写)。
CREATE TABLE `org_wallet` (
  `org_id`        VARCHAR(32)    NOT NULL,
  `balance_cents` BIGINT      NOT NULL DEFAULT 0,
  `frozen_cents`  BIGINT      NOT NULL DEFAULT 0,
  `updated_at`    BIGINT      NOT NULL,
  PRIMARY KEY (`org_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 审批单: 组织内 member 发起 → owner/admin 审批(如组织钱包提现)。
CREATE TABLE `approval` (
  `id`           VARCHAR(32)    NOT NULL,
  `org_id`       VARCHAR(32)    NOT NULL,
  `type`         VARCHAR(16) NOT NULL,
  `ref_id`       VARCHAR(64) NULL,
  `amount_cents` BIGINT      NOT NULL DEFAULT 0,
  `status`       VARCHAR(16) NOT NULL DEFAULT 'pending',
  `created_by`   VARCHAR(32)    NOT NULL,
  `approved_by`  VARCHAR(32)    NULL,
  `note`         VARCHAR(255) NULL,
  `created_at`   BIGINT      NOT NULL,
  `resolved_at`  BIGINT      NULL,
  PRIMARY KEY (`id`),
  KEY `idx_org` (`org_id`),
  KEY `idx_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 开放 API 凭证: Bearer hgk_xxx, 明文仅创建时返回一次, 存储 SHA256(key_hash)。
CREATE TABLE `api_key` (
  `id`         VARCHAR(32)    NOT NULL,
  `org_id`     VARCHAR(32)    NOT NULL,
  `key_hash`   VARCHAR(128) NOT NULL,
  `label`      VARCHAR(128) NULL,
  `scopes`     VARCHAR(128) NULL,
  `status`     VARCHAR(16) NOT NULL DEFAULT 'active',
  `created_at` BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_key_hash` (`key_hash`),
  KEY `idx_org` (`org_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 事件订阅: 任务创建/订单结算/提现审批等事件后台投递。
CREATE TABLE `webhook` (
  `id`         VARCHAR(32)    NOT NULL,
  `org_id`     VARCHAR(32)    NOT NULL,
  `url`        VARCHAR(512) NOT NULL,
  `events`     JSON        NULL,
  `secret`     VARCHAR(128) NULL,
  `status`     VARCHAR(16) NOT NULL DEFAULT 'active',
  `created_at` BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_org` (`org_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Webhook 投递记录(演示用, 落库可追溯)。
CREATE TABLE `webhook_delivery` (
  `id`         VARCHAR(32)    NOT NULL,
  `org_id`     VARCHAR(32)    NOT NULL,
  `webhook_id` VARCHAR(32)    NOT NULL,
  `event`      VARCHAR(32) NOT NULL,
  `status`     VARCHAR(16) NOT NULL DEFAULT 'pending',
  `attempts`   INT         NOT NULL DEFAULT 0,
  `created_at` BIGINT      NOT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_org` (`org_id`),
  KEY `idx_webhook` (`webhook_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 推荐理由(FR-03)。一条 match 最多 3 条理由, 产不出锚点的因子不生成,
-- 所以 rank_no 不保证连续、也不保证有三行。
-- 全列 NOT NULL 且带默认值: 规避 Scan 遇到 NULL 报 converting NULL to string。
CREATE TABLE `match_reason` (
  `id`           VARCHAR(32)  NOT NULL,
  `match_id`     VARCHAR(32)  NOT NULL,
  `rank_no`      TINYINT      NOT NULL DEFAULT 0,
  `text`         VARCHAR(200) NOT NULL DEFAULT '',
  `anchor_type`  VARCHAR(16)  NOT NULL DEFAULT '',
  `anchor_ref`   VARCHAR(64)  NOT NULL DEFAULT '',
  `anchor_label` VARCHAR(120) NOT NULL DEFAULT '',
  `factor`       VARCHAR(16)  NOT NULL DEFAULT '',
  `factor_score` INT          NOT NULL DEFAULT 0,
  `created_at`   BIGINT       NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  KEY `idx_match` (`match_id`, `rank_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
