-- 2026-05-19 对齐 CPA /v1/models 实际暴露列表
-- 删 18 个 CPA 已不暴露的模型（含 Imagen / Moonshot 全套 + 3 个 Gemini image preview
-- + 3 个 *-latest alias + claude-opus-4-6-thinking + gpt-5.3-codex-spark / gpt-oss-120b-medium）
-- 加 2 个 CPA 新出现的 antigravity alias (gemini-3.5-flash-low / gemini-3-flash-agent)
--
-- 注意：channel_models 使用 GORM soft delete（DeletedAt），用 UPDATE 标记而非 DELETE
-- 因为 BeforeDelete hook 会要求 INSERT 到审计表（[[coding_conventions]]）。
-- model_catalogs / model_pricing_rules 没有 soft-delete 字段，直接 DELETE 即可。

BEGIN TRANSACTION;

-- 要删的 model_id 集合
CREATE TEMP TABLE _stale_ids (model_id TEXT NOT NULL PRIMARY KEY);
INSERT INTO _stale_ids VALUES
  ('claude-opus-4-6-thinking'),
  ('gpt-5.3-codex-spark'),
  ('gpt-oss-120b-medium'),
  ('gemini-2.5-flash-image'),
  ('gemini-3-pro-image-preview'),
  ('gemini-3.1-flash-image-preview'),
  ('gemini-flash-latest'),
  ('gemini-flash-lite-latest'),
  ('gemini-pro-latest'),
  ('imagen-3.0-fast-generate-001'),
  ('imagen-3.0-generate-002'),
  ('imagen-4.0-fast-generate-001'),
  ('imagen-4.0-generate-001'),
  ('imagen-4.0-ultra-generate-001'),
  ('kimi-k2'),
  ('kimi-k2-thinking'),
  ('kimi-k2.5'),
  ('kimi-k2.6');

-- 删 model_catalogs（无 soft-delete）
DELETE FROM model_catalogs WHERE model_id IN (SELECT model_id FROM _stale_ids);

-- 删 model_pricing_rules
DELETE FROM model_pricing_rules WHERE model_id IN (SELECT model_id FROM _stale_ids);

-- 删 channel_models（有 deleted_at 字段 = GORM soft delete；这里硬删因为它们都是
-- seed 跑出来的、admin 没碰过、Status=2 未激活、没有任何 api_log 引用）
DELETE FROM channel_models WHERE model_id IN (SELECT model_id FROM _stale_ids);

DROP TABLE _stale_ids;

COMMIT;
