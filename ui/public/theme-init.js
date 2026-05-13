// Runs before React mounts to prevent a light-to-dark flash while keeping CSP strict.
(function () {
  try {
    var pref = localStorage.getItem('daof_theme_preference') || 'dark';
    var dark = pref === 'dark' ||
      (pref === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches);
    if (dark) document.documentElement.classList.add('dark');
  } catch (_) {
    // Ignore storage/privacy-mode failures. The app will apply the theme after mount.
  }
})();
