/**
 * Fluent Reveal Highlight
 *
 * 模拟 Win11 / Microsoft Store 的两种鼠标交互效果：
 *   1. **Border Reveal**：鼠标在元素上方移动时，沿鼠标位置的边框出现"蜡烛光"光晕，
 *      距离越远越暗（用 radial-gradient + mask 在 ::before 实现）。
 *   2. **Content Reveal**：鼠标在元素内部时，鼠标位置出现一个柔和的内层光斑（::after）。
 *
 * 实现：
 *   - 全局 pointermove listener（document 级，事件代理）
 *   - 找到目标元素的 .fl-reveal 祖先，setProperty --mx / --my 为相对元素的鼠标坐标
 *   - rAF 节流，避免每帧多次 setProperty
 *
 * 不依赖任何库，零内存占用（不创建额外 DOM）。
 */

let pendingFrame = null;
let lastEvent = null;

// 任何带这些类的元素都接收鼠标位置变量
const REVEAL_SELECTOR = '.fl-reveal, .fl-hero';

const apply = () => {
  pendingFrame = null;
  if (!lastEvent) return;
  const e = lastEvent;
  const target = e.target?.closest?.(REVEAL_SELECTOR);
  if (!target) return;
  const rect = target.getBoundingClientRect();
  const x = e.clientX - rect.left;
  const y = e.clientY - rect.top;
  target.style.setProperty('--mx', `${x}px`);
  target.style.setProperty('--my', `${y}px`);
};

const onMove = (e) => {
  lastEvent = e;
  if (pendingFrame == null) {
    pendingFrame = requestAnimationFrame(apply);
  }
};

if (typeof window !== 'undefined') {
  window.addEventListener('pointermove', onMove, { passive: true });
}
