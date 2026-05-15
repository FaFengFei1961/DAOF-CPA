import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import react from 'eslint-plugin-react'
import { defineConfig, globalIgnores } from 'eslint/config'
import designSystem from './eslint-plugins/no-design-violation.js'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{js,jsx}'],
    plugins: {
      'design-system': designSystem,
      react,
    },
    extends: [
      js.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    settings: {
      react: { version: 'detect' },
    },
    languageOptions: {
      ecmaVersion: 2020,
      // Phase 6：logger.js 等用 process.env.NODE_ENV，需 node globals
      globals: { ...globals.browser, ...globals.node },
      parserOptions: {
        ecmaVersion: 'latest',
        ecmaFeatures: { jsx: true },
        sourceType: 'module',
      },
    },
    rules: {
      // Enforce design system tokens
      'design-system/strict-tokens': 'error',

      // 防止 JSX 标签 undef（之前漏过 <DataTable> <TextInput> 等 import 缺失导致运行时
      // ReferenceError，整页被 React ErrorBoundary 兜住）。js.configs.recommended 的 no-undef
      // 不识别 JSX 元素引用，必须用 react/jsx-no-undef 补足。
      'react/jsx-no-undef': 'error',
      'react/jsx-uses-vars': 'error',
      // React 17+ 新 JSX transform 不需要 import React in scope
      'react/react-in-jsx-scope': 'off',
      'react/jsx-uses-react': 'off',

      // Phase 6：放宽 catch 未用 err（业内 React 大量 catch (err) {} 模式）+
      // PascalCase 参数（如 destructure prop `({ icon: Icon })`）通常是组件/HOC 引用，
      // ESLint 对 JSX `<Icon />` 用法识别有时不准确，allow PascalCase 跳过避免误报。
      'no-unused-vars': ['error', {
        varsIgnorePattern: '^[A-Z_]',
        argsIgnorePattern: '^[A-Z_]',
        caughtErrors: 'none',
      }],

      // Phase 6（ccg 审查反馈）：React 19 实验编译器规则（eslint-plugin-react-hooks v7）
      // 在常见 fetch+setState 场景误报严重。降级为 warn 而不是 error（保留可见性但不破坏
      // lint pass）。彻底修复需 React Query/SWR 改造，超出当前 phase 范围。
      'react-hooks/set-state-in-effect': 'warn',
      'react-hooks/exhaustive-deps': 'warn',
      // 同样降级的 React 19 编译器规则
      'react-hooks/use-memo': 'warn',
      'react-hooks/refs': 'warn',
      'react-hooks/purity': 'warn',
      // logger.js 显式调 console.error/warn — 不该当 lint error
      'no-console': 'off',
    },
  },
  {
    // Context / hook + component 共存的文件 react-refresh 规则不适用（设计上必须共存）
    files: [
      'src/context/**/*.{js,jsx}',
      'src/hooks/**/*.{js,jsx}',
      'src/components/ui/ChartContainer.jsx',
      'src/routes.jsx',
    ],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
  {
    // Block B exclude these from strict-tokens check
    files: [
      'src/components/ui/**/*.{js,jsx}',
      'src/context/ConfirmContext.jsx',
      'src/utils/theme-seeds.js'
    ],
    rules: {
      'design-system/strict-tokens': 'off',
    },
  },
])
