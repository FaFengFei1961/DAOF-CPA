import React from 'react';
import { useTranslation } from 'react-i18next';
import { ShieldCheck, Activity } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import TextInput from '../../../components/ui/TextInput';
import Select from '../../../components/ui/Select';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar } from './_AdminFormPrimitives';

/**
 * RiskPage — 注册体感与风控引擎（Phase 4 抽出）
 *
 * 替换 Settings.jsx 内 activeTab === 'risk'。三块 form：
 *   1. 注册策略 + IP 上限 + 用户总量上限
 *   2. 新用户 / 拉新激励配置（signup_bonus / referrer_bonus / referee_bonus）
 *   3. 号池采集器配置（credits_refresh_interval / max_retries / retry_interval）
 */
const RiskPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.RISK_TITLE', '注册策略与风控引擎')}
        sub={t('SETTINGS.RISK_DESC', '控制新用户注册的风控策略、IP 上限、平台容量上限和拉新激励')}
        icon={ShieldCheck}
      />

      {/* ─── 注册策略 ────────────────── */}
      <Section title="注册策略">
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/30 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium">{t('SETTINGS.RISK_STRATEGY_LABEL', '注册策略')}</span>
            <span className="text-xs text-on-surface-variant">{t('SETTINGS.RISK_STRATEGY_DESC', '不同策略对新用户的限制松紧不同')}</span>
          </div>
          <div className="w-full md:w-64">
            <Select
              value={configs.reg_strategy || 'dynamic'}
              onChange={(e) => handleChange('reg_strategy', e.target.value)}
              options={[
                { value: 'trust', label: t('SETTINGS.STRATEGY_TRUST', '宽松（信任模式）') },
                { value: 'dynamic', label: t('SETTINGS.STRATEGY_DYNAMIC', '动态（推荐）') },
                { value: 'strict', label: t('SETTINGS.STRATEGY_STRICT', '严格') }
              ]}
            />
          </div>
        </div>

        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/30 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium">{t('SETTINGS.IP_LIMIT_LABEL', '同 IP 注册上限')}</span>
            <span className="text-xs text-on-surface-variant">{t('SETTINGS.IP_LIMIT_DESC', '同一 IP 24h 内最多允许注册的账号数')}</span>
          </div>
          <div className="relative w-full md:w-32">
            <TextInput
              type="number"
              value={configs.reg_ip_limit || '3'}
              onChange={(e) => handleChange('reg_ip_limit', e.target.value)}
              className="text-right"
              style={{ paddingRight: '2.5rem' }}
            />
            <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">{t('SETTINGS.UNIT_COUNT', '次')}</span>
          </div>
        </div>

        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium">平台用户总量上限</span>
            <span className="text-xs text-on-surface-variant">达到上限后停止接受新用户注册（仅统计普通用户，不含管理员）。设为 0 表示无限制。</span>
          </div>
          <div className="relative w-full md:w-32">
            <TextInput
              type="number" min="0"
              value={configs.max_users ?? '0'}
              onChange={(e) => handleChange('max_users', e.target.value)}
              placeholder="0"
              className="text-right"
              style={{ paddingRight: '2.5rem' }}
            />
            <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">人</span>
          </div>
        </div>
      </Section>

      {/* ─── 新用户 / 拉新激励 ─────────── */}
      <Section
        title="新用户奖励 / 拉新激励配置"
        sub={<>所有金额按 USD 计；填 0 表示该项不发放。拉新链接格式：<span className="font-mono text-primary">https://your-domain/?ref=&lt;推荐人用户名&gt;</span></>}
        icon={ShieldCheck}
      >
        {[
          { key: 'signup_bonus',   label: '新用户初始金额（signup_bonus）', hint: '每个新注册用户开局送多少额度（无论是否带 ref）。', placeholder: '1.00', defaultVal: '1' },
          { key: 'referrer_bonus', label: '拉新者奖励（referrer_bonus）',   hint: '推荐人通过 ?ref=自己用户名 的链接成功带来一个新用户，给推荐人加多少额度。', placeholder: '0.50', defaultVal: '0' },
          { key: 'referee_bonus',  label: '被拉新者奖励（referee_bonus）', hint: '通过推荐链接进来的新用户，除了 signup_bonus 外**额外**多送多少额度（叠加，不替换）。', placeholder: '0.30', defaultVal: '0' },
        ].map((item, idx, arr) => (
          <div key={item.key} className={`flex flex-col md:flex-row md:items-center justify-between py-3 ${idx === arr.length - 1 ? '' : 'border-b border-outline-variant/20'} gap-3`}>
            <div className="flex flex-col gap-1 w-full md:w-2/3">
              <span className="text-on-surface-variant font-medium text-sm">{item.label}</span>
              <span className="text-xs text-outline">{item.hint}</span>
            </div>
            <div className="relative w-full md:w-32">
              <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none z-10">$</span>
              <TextInput
                type="number" step="0.01" min="0"
                value={configs[item.key] ?? item.defaultVal}
                onChange={(e) => handleChange(item.key, e.target.value)}
                placeholder={item.placeholder}
                className="pl-7 text-right"
              />
            </div>
          </div>
        ))}
      </Section>

      {/* ─── 号池采集器 ─────────────────── */}
      <Section
        title="号池额度采集器"
        sub={<>控制后台 goroutine 多久轮询一次上游账号池的剩余额度，决定<span className="text-primary font-mono"> 号池监控</span> 看板与用户首页号池卡片的数据新鲜度。</>}
        icon={Activity}
      >
        {[
          { key: 'credits_refresh_interval', label: '全量刷新周期', hint: '每隔多少分钟把所有上游账号的额度全量重新拉一遍。建议 10-30 分钟，过短会被上游限流。', unit: '分钟', placeholder: '15', defaultVal: '15', min: 1, max: 1440 },
          { key: 'credits_max_retries', label: '失败重试次数', hint: <>单个上游账号连续失败时最多重试几次后放弃，等下一轮全量周期。<span className="text-warning font-mono">0</span> = 无限重试，仍带指数退避（封顶 60 分钟）防止雪崩。</>, unit: '次', placeholder: '3', defaultVal: '3', min: 0, max: 100 },
          { key: 'credits_retry_interval', label: '重试间隔（基础值）', hint: <>每次重试之间等待多少分钟。<span className="text-warning">实际间隔会按指数退避（base × 2^retry_count），封顶 60 分钟</span>，避免上游持续被冲击。</>, unit: '分钟', placeholder: '5', defaultVal: '5', min: 1, max: 1440 },
        ].map((item, idx, arr) => (
          <div key={item.key} className={`flex flex-col md:flex-row md:items-center justify-between py-3 ${idx === arr.length - 1 ? '' : 'border-b border-outline-variant/20'} gap-3`}>
            <div className="flex flex-col gap-1 w-full md:w-2/3">
              <span className="text-on-surface-variant font-medium text-sm">{item.label}</span>
              <span className="text-xs text-outline">{item.hint}</span>
            </div>
            <div className="relative w-full md:w-32">
              <TextInput
                type="number" min={item.min} max={item.max}
                value={configs[item.key] ?? item.defaultVal}
                onChange={(e) => handleChange(item.key, e.target.value)}
                placeholder={item.placeholder}
                className="text-right"
                style={{ paddingRight: '3rem' }}
              />
              <span className="absolute right-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">{item.unit}</span>
            </div>
          </div>
        ))}
      </Section>

      <SaveBar loading={loading} onSave={handleSave} />
    </PageContainer>
  );
};

export default RiskPage;
