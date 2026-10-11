// Finance Tracker - fit figures to their readout.
//
// A figure marked [data-fit] never wraps (see .lcd-value in app.css). The CSS
// sizes it from its container's width; when a long amount would still overflow
// its well, this script lowers the font size until it fits. It measures and
// scales text only: it reads no number and computes nothing about money.
(function () {
  'use strict';

  var MIN_PX = 11;

  function fit(el) {
    el.style.fontSize = '';
    var available = el.clientWidth;
    var needed = el.scrollWidth;
    if (!available || needed <= available) {
      return;
    }
    var size = parseFloat(window.getComputedStyle(el).fontSize);
    var next = Math.max(MIN_PX, Math.floor(size * available / needed * 10) / 10);
    el.style.fontSize = next + 'px';
    // A second pass absorbs the rounding of unit labels sized in em.
    if (el.scrollWidth > el.clientWidth && next > MIN_PX) {
      el.style.fontSize = Math.max(MIN_PX, next - 1) + 'px';
    }
  }

  function fitAll(root) {
    var scope = root && root.querySelectorAll ? root : document;
    var els = scope.querySelectorAll('[data-fit]');
    for (var i = 0; i < els.length; i++) {
      fit(els[i]);
    }
  }

  var observer = null;
  if ('ResizeObserver' in window) {
    observer = new ResizeObserver(function (entries) {
      for (var i = 0; i < entries.length; i++) {
        var els = entries[i].target.querySelectorAll('[data-fit]');
        for (var j = 0; j < els.length; j++) {
          fit(els[j]);
        }
      }
    });
  }

  function watch(root) {
    var scope = root && root.querySelectorAll ? root : document;
    var els = scope.querySelectorAll('[data-fit]');
    for (var i = 0; i < els.length; i++) {
      if (observer && els[i].parentElement) {
        observer.observe(els[i].parentElement);
      }
    }
    fitAll(scope);
  }

  function init() {
    watch(document);
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(function () { fitAll(document); });
    }
    // htmx swaps a dashboard block in place: fit what arrived.
    document.body.addEventListener('htmx:afterSettle', function (e) {
      watch(e.target);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
