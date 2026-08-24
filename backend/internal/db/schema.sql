-- SQLite schema for OVH 控制台 server
-- 设计原则：
--   1. 列式字段用普通列（便于索引 / WHERE / ORDER BY）
--   2. 复杂嵌套（数组 / map / 对象）用 TEXT 列存 JSON
--   3. bool 用 INTEGER 0/1
--   4. 时间字段保持原 JSON 的字符串格式（ISO8601）以便兼容前端
--   5. 所有 CREATE 都用 IF NOT EXISTS，启动时无脑跑一遍

-- ===========================================
-- kv: 单例数据（Config / monitor & vps 全局状态）
-- ===========================================
CREATE TABLE IF NOT EXISTS kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- ===========================================
-- ovh_accounts: OVH 多账户凭据
-- 每条记录代表一个 OVH 账户;is_default=1 的那条用于未指定账户时 fallback
-- queue / history / config_sniper_tasks 的 account_id 引用这里
-- ===========================================
CREATE TABLE IF NOT EXISTS ovh_accounts (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  endpoint     TEXT NOT NULL,
  zone         TEXT NOT NULL,
  app_key      TEXT NOT NULL,
  app_secret   TEXT NOT NULL,
  consumer_key TEXT NOT NULL,
  iam          TEXT NOT NULL,
  is_default   INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ovh_accounts_default ON ovh_accounts(is_default);

-- ===========================================
-- queue: 抢购队列任务
-- ===========================================
CREATE TABLE IF NOT EXISTS queue (
  id                     TEXT PRIMARY KEY,
  plan_code              TEXT NOT NULL,
  datacenter             TEXT NOT NULL,
  options                TEXT NOT NULL DEFAULT '[]', -- JSON 数组
  status                 TEXT NOT NULL,
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL,
  retry_interval         INTEGER NOT NULL DEFAULT 60,
  retry_count            INTEGER NOT NULL DEFAULT 0,
  max_retries            INTEGER NOT NULL DEFAULT 0,
  last_check_time        REAL    NOT NULL DEFAULT 0,
  quick_order            INTEGER NOT NULL DEFAULT 0,
  priority               INTEGER NOT NULL DEFAULT 0,
  from_telegram          INTEGER NOT NULL DEFAULT 0,
  config_sniper_task_id  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_queue_status     ON queue(status);
CREATE INDEX IF NOT EXISTS idx_queue_plan_code  ON queue(plan_code);

-- ===========================================
-- history: 抢购历史记录
-- ===========================================
CREATE TABLE IF NOT EXISTS history (
  id              TEXT PRIMARY KEY,
  task_id         TEXT NOT NULL DEFAULT '',
  plan_code       TEXT NOT NULL,
  datacenter      TEXT NOT NULL,
  options         TEXT NOT NULL DEFAULT '[]', -- JSON
  status          TEXT NOT NULL,
  order_id        TEXT NOT NULL DEFAULT '',
  order_url       TEXT NOT NULL DEFAULT '',
  error_message   TEXT,                       -- nullable
  purchase_time   TEXT NOT NULL,
  attempt_count   INTEGER NOT NULL DEFAULT 0,
  expiration_time TEXT NOT NULL DEFAULT '',
  price           TEXT                        -- JSON nullable (PriceInfo)
);
CREATE INDEX IF NOT EXISTS idx_history_status        ON history(status);
CREATE INDEX IF NOT EXISTS idx_history_purchase_time ON history(purchase_time DESC);
CREATE INDEX IF NOT EXISTS idx_history_task_id       ON history(task_id);
CREATE INDEX IF NOT EXISTS idx_history_plan_code     ON history(plan_code);

-- ===========================================
-- servers: OVH 服务器目录缓存（refresh-from-OVH 整块覆盖式 upsert）
-- 数据本身就是 OVH 接口返回的 catalog，结构复杂且字段稳定，整块 JSON 存即可
-- ===========================================
CREATE TABLE IF NOT EXISTS servers (
  plan_code  TEXT PRIMARY KEY,
  data       TEXT NOT NULL,   -- 完整 ServerPlan JSON
  updated_at INTEGER NOT NULL -- Unix epoch ms
);

-- ===========================================
-- monitor_subscriptions: 服务器补货监控订阅
-- ===========================================
CREATE TABLE IF NOT EXISTS monitor_subscriptions (
  plan_code           TEXT PRIMARY KEY,
  datacenters         TEXT NOT NULL DEFAULT '[]',  -- JSON []string
  notify_available    INTEGER NOT NULL DEFAULT 1,
  notify_unavailable  INTEGER NOT NULL DEFAULT 0,
  last_status         TEXT NOT NULL DEFAULT '{}',  -- JSON map[string]string
  created_at          TEXT NOT NULL,
  history             TEXT NOT NULL DEFAULT '[]',  -- JSON []HistoryEntry
  server_name         TEXT NOT NULL DEFAULT '',
  auto_order          INTEGER NOT NULL DEFAULT 0,
  quantity            INTEGER NOT NULL DEFAULT 1
);

-- ===========================================
-- vps_subscriptions: VPS 补货监控订阅
-- ===========================================
CREATE TABLE IF NOT EXISTS vps_subscriptions (
  id                  TEXT PRIMARY KEY,
  plan_code           TEXT NOT NULL,
  ovh_subsidiary      TEXT NOT NULL DEFAULT '',
  datacenters         TEXT NOT NULL DEFAULT '[]',  -- JSON []string
  monitor_linux       INTEGER NOT NULL DEFAULT 0,
  monitor_windows     INTEGER NOT NULL DEFAULT 0,
  notify_available    INTEGER NOT NULL DEFAULT 1,
  notify_unavailable  INTEGER NOT NULL DEFAULT 0,
  last_status         TEXT NOT NULL DEFAULT '{}',  -- JSON map
  history             TEXT NOT NULL DEFAULT '[]',  -- JSON []
  created_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vps_plan_code ON vps_subscriptions(plan_code);

-- ===========================================
-- catalogs: OVH 公开 catalog 每个 subsidiary 一份
-- 用途：浏览页"价格"显示。catalog 单份 2-5MB，直连 OVH 要 1-3s，缓存到本地后毫秒级返回
-- ===========================================
CREATE TABLE IF NOT EXISTS catalogs (
  subsidiary TEXT PRIMARY KEY,
  data       TEXT NOT NULL,   -- 完整 catalog JSON
  updated_at INTEGER NOT NULL -- Unix epoch ms
);

-- ===========================================
-- availability_snapshots: OVH 实时可用性整点快照
-- 每个区域每次后台刷新保存一份，应用层只保留最近 7 天。
-- ===========================================
CREATE TABLE IF NOT EXISTS availability_snapshots (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  region     TEXT NOT NULL,
  fetched_at INTEGER NOT NULL, -- Unix epoch ms
  item_count INTEGER NOT NULL DEFAULT 0,
  data       TEXT NOT NULL      -- 完整可用性数组 JSON
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_availability_snapshots_region_time
  ON availability_snapshots(region, fetched_at);
CREATE INDEX IF NOT EXISTS idx_availability_snapshots_fetched_at
  ON availability_snapshots(fetched_at DESC);

-- ===========================================
-- server_plan_snapshots: 整点比对使用的区域服务器目录 planCode 快照
-- 与 availability_snapshots 使用同一批次时间写入，按区域只保存最近 7 天。
-- data 保存已经标准化、去重、排序后的 planCode JSON 数组。
-- ===========================================
CREATE TABLE IF NOT EXISTS server_plan_snapshots (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  region     TEXT NOT NULL,
  fetched_at INTEGER NOT NULL, -- Unix epoch ms；与对应实时可用性快照相同
  item_count INTEGER NOT NULL DEFAULT 0,
  data       TEXT NOT NULL      -- planCode JSON 数组
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_server_plan_snapshots_region_time
  ON server_plan_snapshots(region, fetched_at);
CREATE INDEX IF NOT EXISTS idx_server_plan_snapshots_fetched_at
  ON server_plan_snapshots(fetched_at DESC);

-- ===========================================
-- preadded_servers: 实时可用性中存在但服务器目录没有的条目
-- 旧版兼容明细表；新流程使用同批次原子写入的 preadded_server_results。
-- ===========================================
CREATE TABLE IF NOT EXISTS preadded_servers (
  region      TEXT NOT NULL,
  fqn         TEXT NOT NULL,
  plan_code   TEXT NOT NULL,
  detected_at INTEGER NOT NULL,
  data        TEXT NOT NULL,
  PRIMARY KEY (region, fqn)
);
CREATE INDEX IF NOT EXISTS idx_preadded_servers_detected_at
  ON preadded_servers(detected_at DESC);

-- ===========================================
-- preadded_server_results: 后台比对后按区域 + planCode 聚合的页面结果
-- 页面只读取这张小表，不再下载并处理全部 FQN 原始配置。
-- ===========================================
CREATE TABLE IF NOT EXISTS preadded_server_results (
  region      TEXT NOT NULL,
  plan_code   TEXT NOT NULL,
  compared_at INTEGER NOT NULL,
  search_text TEXT NOT NULL DEFAULT '',
  data        TEXT NOT NULL,
  PRIMARY KEY (region, plan_code)
);
CREATE INDEX IF NOT EXISTS idx_preadded_server_results_compared_at
  ON preadded_server_results(compared_at DESC);

-- 即使某次比对结果为 0 条，也单独保存成功完成时间。
CREATE TABLE IF NOT EXISTS preadded_server_comparisons (
  region      TEXT PRIMARY KEY,
  compared_at INTEGER NOT NULL,
  item_count  INTEGER NOT NULL DEFAULT 0
);

-- (旧:config_sniper_tasks 表已删除,功能下线。老数据库残留的该表 / config_sniper_task_id 列保留不动,
--  无害,无人读写。)

-- ===========================================
-- server_aliases: 服务器本地别名(纯本地,不下发 OVH)
-- 用途:服务器控制 tab 选择器 / 详情页把技术 service_name 显示成用户取的友好名
-- account_id + service_name 复合主键,避免不同账户同 service_name 互相串
-- ===========================================
CREATE TABLE IF NOT EXISTS server_aliases (
  account_id   TEXT NOT NULL,
  service_name TEXT NOT NULL,
  alias        TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  PRIMARY KEY (account_id, service_name)
);
CREATE INDEX IF NOT EXISTS idx_server_aliases_account ON server_aliases(account_id);

-- ===========================================
-- telegram_order_buttons: TG 一键下单按钮 UUID 缓存
-- 按钮 callback_data 只有 UUID（受 Telegram 64 字节限制），完整下单参数存这里
-- 必须持久化：进程重启 / Docker 重建后内存缓存会丢，否则一点击就 400
-- ===========================================
CREATE TABLE IF NOT EXISTS telegram_order_buttons (
  id          TEXT PRIMARY KEY,
  plan_code   TEXT NOT NULL,
  datacenter  TEXT NOT NULL,
  options     TEXT NOT NULL DEFAULT '[]',  -- JSON []string
  config_info TEXT NOT NULL DEFAULT '{}', -- JSON object
  created_at  REAL NOT NULL,              -- unix seconds
  used_at     REAL NOT NULL DEFAULT 0     -- >0 表示已消费（一次性 nonce）
);
CREATE INDEX IF NOT EXISTS idx_tg_buttons_created ON telegram_order_buttons(created_at);

-- ===========================================
-- telegram_updates: webhook update_id 幂等去重
-- ===========================================
CREATE TABLE IF NOT EXISTS telegram_updates (
  update_id    INTEGER PRIMARY KEY,
  processed_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tg_updates_processed ON telegram_updates(processed_at);

-- 飞书事件会自动重试投递，按 event_id 幂等去重，避免重复执行下单命令。
CREATE TABLE IF NOT EXISTS feishu_events (
  event_id     TEXT PRIMARY KEY,
  processed_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_feishu_events_processed ON feishu_events(processed_at);

-- ===========================================
-- 微信 iLink Bot：凭据、长轮询游标、会话上下文、去重与单实例租约
-- bot_token 只保存在 SQLite，不通过通用 settings API 返回前端。
-- ===========================================
CREATE TABLE IF NOT EXISTS weixin_credentials (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  account_id TEXT NOT NULL,
  bot_token  TEXT NOT NULL,
  base_url   TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS weixin_sync_state (
  account_id TEXT PRIMARY KEY,
  sync_buf   TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS weixin_context_tokens (
  account_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  context_token TEXT NOT NULL,
  updated_at    INTEGER NOT NULL,
  PRIMARY KEY (account_id, user_id)
);

CREATE TABLE IF NOT EXISTS weixin_seen_messages (
  message_key TEXT PRIMARY KEY,
  seen_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_weixin_seen_at ON weixin_seen_messages(seen_at);

CREATE TABLE IF NOT EXISTS weixin_runtime_locks (
  lock_name  TEXT PRIMARY KEY,
  owner_id   TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);
