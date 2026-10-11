// Finance Tracker - Theme Toggle (Aluminio light <-> Grafito dark, TD-071)
(function () {
  'use strict';

  // Apply theme immediately to prevent FOUC
  var storedTheme = localStorage.getItem('theme') || 'light';
  document.documentElement.setAttribute('data-theme', storedTheme);

  var SUN_ICON = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="5"></circle><line x1="12" y1="1" x2="12" y2="3"></line><line x1="12" y1="21" x2="12" y2="23"></line><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"></line><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"></line><line x1="1" y1="12" x2="3" y2="12"></line><line x1="21" y1="12" x2="23" y2="12"></line><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"></line><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"></line></svg>';
  var MOON_ICON = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path></svg>';

  function updateThemeUI(theme) {
    var isDark = theme === 'dark' || theme === 'dim';
    var nextLabel = isDark ? 'Cambiar a acabado Aluminio (claro)' : 'Cambiar a acabado Grafito (oscuro)';
    var iconHtml = isDark ? SUN_ICON : MOON_ICON;

    var buttons = document.querySelectorAll('.theme-toggle-btn');
    buttons.forEach(function (btn) {
      btn.setAttribute('aria-label', nextLabel);
      btn.setAttribute('title', nextLabel);
      var iconWrap = btn.querySelector('.theme-icon');
      if (iconWrap) {
        iconWrap.innerHTML = iconHtml;
      }
    });

    var metaTheme = document.querySelector('meta[name="theme-color"]');
    if (metaTheme) {
      metaTheme.setAttribute('content', isDark ? '#26282b' : '#dcd8cf');
    }
  }

  function toggleTheme() {
    var current = document.documentElement.getAttribute('data-theme') || 'light';
    var next = (current === 'dark' || current === 'dim') ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    localStorage.setItem('theme', next);
    updateThemeUI(next);
  }

  // Expose globally
  window.toggleFinanceTheme = toggleTheme;

  function init() {
    var currentTheme = localStorage.getItem('theme') || 'light';
    document.documentElement.setAttribute('data-theme', currentTheme);
    updateThemeUI(currentTheme);

    // Attach click listener
    document.addEventListener('click', function (e) {
      var btn = e.target.closest('.theme-toggle-btn');
      if (btn) {
        e.preventDefault();
        toggleTheme();
      }
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
