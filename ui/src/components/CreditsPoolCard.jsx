import React, { useState, useCallback, useMemo } from 'react';
import { Layers, ChevronDown, ChevronUp, Sparkles, Zap, Cpu, Activity, Clock } from 'lucide-react';
import { remainingColor, safePct } from '../utils/credits';
import { useCreditsPoolSummary } from '../hooks/useCreditsPoolSummary';

// 普通用户首页号池卡片 —— 只展示按模型聚合后的剩余百分比，不暴露 auth_index / providers / 邮箱
//
// 数据源：GET /api/credits-pool/summary （后端按 IP 限流 6 次/分钟）
// fix Major Codex UX 审查（第二十五轮）：从 useCreditsPoolSummary 共享 hook 取，
// 避免与 Dashboard 双轮询消耗后端配额。

const COLLAPSE_KEY = 'daof_credits_card_collapsed';

// 根据模型名前缀挑图标
const pickIcon = (modelName) => {
  const m = (modelName || '').toLowerCase();
  if (m.startsWith('claude') || m.startsWith('anthropic')) return Sparkles;
  if (m.startsWith('gemini')) return Zap;
  if (m.startsWith('gpt') || m.startsWith('codex')) return Cpu;
  if (m.startsWith('kimi')) return Activity;
  return Layers;
};

const ModelRow = React.memo(function ModelRow({ model }) {
  const Icon = pickIcon(model.model_name);
  const rem = safePct(model.avg_remaining_pct);
  const color = remainingColor(rem);
  // 后端只暴露 online 布尔，不再回送凭证数量
  const offline = !model.online;

  return (
    <div className="px-4 py-3 border-b border-outline-variant/30 last:border-b-0">
      <div className="flex items-center justify-between gap-3 mb-2">
        <div className="flex items-center gap-2 min-w-0 flex-1">
          <Icon size={14} className="text-on-surface-variant shrink-0" />
          <span className="font-mono text-sm text-on-surface truncate" title={model.model_name}>
            {model.model_name}
          </span>
        </div>
        <div className="shrink-0 flex items-center gap-2 text-xs">
          {/* 用户界面只展示该模型的平均剩余比例；不暴露凭证数量等内部基础设施细节 */}
          <span className="font-mono font-semibold w-14 text-right" style={{ color }}>
            {offline ? '离线' : `${rem.toFixed(1)}%`}
          </span>
        </div>
      </div>
      <div className="h-1.5 rounded-full bg-black/40 overflow-hidden border border-outline-variant/30">
        <div
          className="h-full transition-all duration-500"
          style={{ width: `${rem}%`, background: color, boxShadow: offline ? 'none' : `0 0 8px ${color}80` }}
        />
      </div>
    </div>
  );
}, (prev, next) => {
  // 父组件每次轮询都会从 setData 创建新 model 引用，默认浅比较会失效。
  // 自定义比较器：只看实际渲染相关字段是否变化，避免 60s 一次的全量重绘。
  const a = prev.model, b = next.model;
  return a.model_name === b.model_name
    && a.online === b.online
    && a.avg_remaining_pct === b.avg_remaining_pct;
});

const CreditsPoolCard = () => {
  // fix Major Codex UX 审查（第二十五轮）：共享 hook 替代独立轮询
  const { models: hookModels, loadedAt } = useCreditsPoolSummary();
  const data = useMemo(() => ({
    models: hookModels,
    last_full: loadedAt ? new Date(loadedAt).toISOString() : '',
    stale: false,
  }), [hookModels, loadedAt]);
  const loading = !loadedAt && hookModels.length === 0;
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem(COLLAPSE_KEY) === '1';
    } catch {
      return false;
    }
  });

  // fix Major Codex UX 审查（第二十五轮）：data/loading/轮询全部交给 useCreditsPoolSummary，
  // 原本 ref + consecutive-failure + stopPolling 逻辑改由 hook 内部 + document.hidden 兜底。

  // 切换折叠时同步持久化
  const toggleCollapsed = useCallback(() => {
    setCollapsed((prev) => {
      const next = !prev;
      try {
        localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0');
      } catch {
        /* localStorage 不可用时静默忽略 */
      }
      return next;
    });
  }, []);

  // 排序：在线优先，再按平均剩余降序
  const orderedModels = useMemo(() => {
    return [...(data.models || [])].sort((a, b) => {
      if (a.online !== b.online) return a.online ? -1 : 1;
      return (b.avg_remaining_pct || 0) - (a.avg_remaining_pct || 0);
    });
  }, [data.models]);

  // 整体状态："所有模型都不在线" 触发醒目的全离线提示
  const allOffline = useMemo(() => {
    if (!orderedModels.length) return false;
    return orderedModels.every(m => !m.online);
  }, [orderedModels]);

  // 没数据时不渲染（用户首页不展示空卡片）
  if (loading) return null;
  if (!orderedModels.length) return null;

  const lastFullStr = data.last_full && data.last_full !== '0001-01-01T00:00:00Z'
    ? new Date(data.last_full).toLocaleTimeString('zh-CN', { hour12: false })
    : '采集中';

  return (
    <div className="bg-surface-container border border-outline-variant rounded-2xl overflow-hidden mb-6">
      <button
        onClick={toggleCollapsed}
        aria-expanded={!collapsed}
        aria-controls="credits-pool-card-list"
        aria-label={collapsed ? '展开号池剩余额度卡片' : '折叠号池剩余额度卡片'}
        className="w-full flex items-center justify-between px-5 py-4 hover:bg-surface-container-high transition-colors"
      >
        <div className="flex items-center gap-3">
          <div className="w-9 h-9 rounded-lg bg-primary/10 border border-primary/30 flex items-center justify-center">
            <Layers size={16} className="text-primary" />
          </div>
          <div className="text-left">
            <h3 className="text-sm font-bold text-on-surface">平台号池剩余额度</h3>
            <div className="flex items-center gap-2 text-xs text-on-surface-variant mt-0.5">
              {allOffline ? (
                <span className="text-red-400">当前所有节点均不可用</span>
              ) : (
                <span className="text-emerald-400">运行中</span>
              )}
              <span className="text-outline">·</span>
              <span className="inline-flex items-center gap-1">
                <Clock size={10} />
                {lastFullStr}
              </span>
              {data.stale && (
                <span className="text-amber-400 ml-1">· 数据可能过期</span>
              )}
            </div>
          </div>
        </div>
        <div className="text-on-surface-variant" aria-hidden="true">
          {collapsed ? <ChevronDown size={18} /> : <ChevronUp size={18} />}
        </div>
      </button>

      {!collapsed && (
        <div
          id="credits-pool-card-list"
          className="border-t border-outline-variant/40 max-h-96 overflow-y-auto"
        >
          {orderedModels.map((m) => (
            <ModelRow key={m.model_name} model={m} />
          ))}
        </div>
      )}
    </div>
  );
};

export default CreditsPoolCard;
