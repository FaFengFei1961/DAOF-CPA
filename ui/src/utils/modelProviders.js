import {
  Bot,
  BrainCircuit,
  Code,
  Cpu,
  Image as ImageIcon,
  MessageSquare,
  Sparkles,
  Zap,
} from 'lucide-react';

export const OPENAI_O_SERIES_RE = /\bo\d+\b/;

export const PROVIDER_META = {
  Anthropic: { name: 'Anthropic', hue: '#d97706', icon: BrainCircuit, order: 10 },
  OpenAI: { name: 'OpenAI', hue: '#10b981', icon: Bot, order: 20 },
  Google: { name: 'Google', hue: '#0ea5e9', icon: Sparkles, order: 30 },
  DeepSeek: { name: 'DeepSeek', hue: '#3b82f6', icon: Code, order: 40 },
  Qwen: { name: 'Qwen', hue: '#6366f1', icon: Cpu, order: 50 },
  Meta: { name: 'Meta', hue: '#60a5fa', icon: Cpu, order: 60 },
  xAI: { name: 'xAI', hue: '#facc15', icon: Zap, order: 70 },
  Moonshot: { name: 'Moonshot', hue: '#ef4444', icon: MessageSquare, order: 80 },
  Midjourney: { name: 'Midjourney', hue: '#a855f7', icon: ImageIcon, order: 90 },
  Other: { name: 'Other', hue: '#94a3b8', icon: Sparkles, order: 1000 },
};

export const inferModelProvider = (modelId = '') => {
  const id = modelId.toLowerCase();
  if (id.includes('claude') || id.includes('anthropic')) return PROVIDER_META.Anthropic;
  if (id.includes('gpt') || id.includes('openai') || id.includes('codex') || OPENAI_O_SERIES_RE.test(id)) return PROVIDER_META.OpenAI;
  if (id.includes('gemini') || id.includes('google')) return PROVIDER_META.Google;
  if (id.includes('deepseek')) return PROVIDER_META.DeepSeek;
  if (id.includes('qwen') || id.includes('tongyi')) return PROVIDER_META.Qwen;
  if (id.includes('llama') || id.includes('meta')) return PROVIDER_META.Meta;
  if (id.includes('grok') || id.includes('xai')) return PROVIDER_META.xAI;
  if (id.includes('moonshot') || id.includes('kimi')) return PROVIDER_META.Moonshot;
  if (id.includes('midjourney') || id.includes('mj-')) return PROVIDER_META.Midjourney;
  return PROVIDER_META.Other;
};

export const groupModelsByProvider = (models = []) => {
  const groups = new Map();
  for (const model of models) {
    const provider = inferModelProvider(model.model_id || model.model_name || '');
    if (!groups.has(provider.name)) {
      groups.set(provider.name, { provider, items: [] });
    }
    groups.get(provider.name).items.push(model);
  }

  return Array.from(groups.values())
    .map((group) => ({
      ...group,
      items: [...group.items].sort((a, b) => String(a.model_id || '').localeCompare(String(b.model_id || ''))),
    }))
    .sort((a, b) => a.provider.order - b.provider.order || a.provider.name.localeCompare(b.provider.name));
};
