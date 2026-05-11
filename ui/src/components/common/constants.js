// 全局共享 UI 常量
//
// fix MAJOR（gemini 第十六轮）：PAGE_SIZE 不再各组件硬编码（散落 50/30/20），
// 统一从此处 import，方便日后调整。

export const PAGE_SIZE_DEFAULT = 50;
export const PAGE_SIZE_HISTORY = 20; // 充值历史这种"近期为主"的列表用更小页（首屏更快）
