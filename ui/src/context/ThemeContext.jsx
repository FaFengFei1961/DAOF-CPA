import React, { createContext, useContext, useEffect, useState } from 'react';
import { themeFromSourceColor, argbFromHex, hexFromArgb } from '@material/material-color-utilities';

const ThemeContext = createContext();

export const useTheme = () => useContext(ThemeContext);

const applyMD3Theme = (seedColor, isDark) => {
  const root = document.documentElement;
  const theme = themeFromSourceColor(argbFromHex(seedColor));
  const scheme = isDark ? theme.schemes.dark : theme.schemes.light;

  // Map MD3 semantic tokens directly to CSS variables
  const colorMap = {
    '--color-primary': scheme.primary,
    '--color-on-primary': scheme.onPrimary,
    '--color-primary-container': scheme.primaryContainer,
    '--color-on-primary-container': scheme.onPrimaryContainer,
    '--color-secondary': scheme.secondary,
    '--color-on-secondary': scheme.onSecondary,
    '--color-secondary-container': scheme.secondaryContainer,
    '--color-on-secondary-container': scheme.onSecondaryContainer,
    '--color-tertiary': scheme.tertiary,
    '--color-on-tertiary': scheme.onTertiary,
    '--color-tertiary-container': scheme.tertiaryContainer,
    '--color-on-tertiary-container': scheme.onTertiaryContainer,
    '--color-error': scheme.error,
    '--color-on-error': scheme.onError,
    '--color-error-container': scheme.errorContainer,
    '--color-on-error-container': scheme.onErrorContainer,
    '--color-background': scheme.background,
    '--color-on-background': scheme.onBackground,
    '--color-surface': scheme.surface,
    '--color-on-surface': scheme.onSurface,
    '--color-surface-variant': scheme.surfaceVariant,
    '--color-on-surface-variant': scheme.onSurfaceVariant,
    '--color-outline': scheme.outline,
    '--color-outline-variant': scheme.outlineVariant,
    '--color-shadow': scheme.shadow,
    '--color-scrim': scheme.scrim,
    '--color-inverse-surface': scheme.inverseSurface,
    '--color-inverse-on-surface': scheme.inverseOnSurface,
    '--color-inverse-primary': scheme.inversePrimary,
    // Add custom container shades slightly distinct from pure surface for Tailwind styling
    '--color-surface-container-highest': scheme.surfaceContainerHighest,
    '--color-surface-container-high': scheme.surfaceContainerHigh,
    '--color-surface-container': scheme.surfaceContainer,
    '--color-surface-container-low': scheme.surfaceContainerLow,
    '--color-surface-container-lowest': scheme.surfaceContainerLowest,
    '--color-surface-dim': scheme.surfaceDim,
    '--color-surface-bright': scheme.surfaceBright,
  };

  Object.entries(colorMap).forEach(([key, param]) => {
    // If the library version doesn't support the newer surface roles, fallback to standard surface/surfaceVariant
    if (param !== undefined) {
      root.style.setProperty(key, hexFromArgb(param));
    } else {
       // Fallbacks for older @material/material-color-utilities missing surface containers
       if (key.includes('surface-container')) root.style.setProperty(key, hexFromArgb(scheme.surfaceVariant));
    }
  });
};

export const ThemeProvider = ({ children }) => {
  // Theme preference: 'system' | 'dark' | 'light'。默认深色（产品定位）。
  const [themePref, setThemePref] = useState(() => {
    return localStorage.getItem('daof_theme_preference') || 'dark';
  });

  // Seed color: any Hex string。MD3 调色板由此一色种子展开 30+ 衍生色。
  const [seedColor, setSeedColor] = useState(() => {
    // Phase 7.7-2：默认 seed 色 #7c5cff（lavender 偏游戏调）→ #6366f1（Indigo），
    // Linear / Notion / Cursor 同款深紫蓝，开发者向 SaaS 标准选择
    return localStorage.getItem('daof_seed_color') || '#6366f1';
  });

  // Current active mode — 初始值同步推断，避免首屏先白后黑闪光
  const [isDarkMode, setIsDarkMode] = useState(() => {
    const pref = localStorage.getItem('daof_theme_preference') || 'dark';
    if (pref === 'system') {
      return typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches;
    }
    return pref === 'dark';
  });

  useEffect(() => {
    const root = document.documentElement;
    let isDark = true;

    // 浅色模式启用：根据 themePref 决定（system/dark/light）
    if (themePref === 'system') {
      isDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    } else {
      isDark = themePref === 'dark';
    }

    setIsDarkMode(isDark);

    if (isDark) {
      root.classList.add('dark');
    } else {
      root.classList.remove('dark');
    }

    // Apply MD3 CSS variables based on Seed + Phase
    try {
      applyMD3Theme(seedColor, isDark);
    } catch {
      /* MD3 palette gen failed, fallback to default Tailwind colors */
    }
    
    // Listen for System preference changes if set to 'system'
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
    const handleChange = (e) => {
      if (themePref === 'system') {
        setIsDarkMode(e.matches);
        if (e.matches) root.classList.add('dark');
        else root.classList.remove('dark');
        applyMD3Theme(seedColor, e.matches);
      }
    };
    
    mediaQuery.addEventListener('change', handleChange);
    return () => mediaQuery.removeEventListener('change', handleChange);

  }, [themePref, seedColor]);

  // Persist handlers
  const changeTheme = (mode) => {
    setThemePref(mode);
    localStorage.setItem('daof_theme_preference', mode);
  };

  const changeSeedColor = (hex) => {
    setSeedColor(hex);
    localStorage.setItem('daof_seed_color', hex);
  };

  return (
    <ThemeContext.Provider value={{ themePref, changeTheme, seedColor, changeSeedColor, isDarkMode }}>
      {children}
    </ThemeContext.Provider>
  );
};
