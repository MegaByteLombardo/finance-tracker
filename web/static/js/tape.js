// Finance Tracker - Ticker tape (TD-096)
//
// The server renders every group of the tape, in catalogue order. This script
// applies the viewer's choices stored in localStorage -- which groups, in what
// order, what colour, what speed -- and makes the tape loop.
//
// Like fit.js it measures widths only. It never reads, parses or computes a
// figure: every string on the tape arrived formatted from the backend.
(function () {
  'use strict';

  var STORAGE_KEY = 'ft.ticker';
  var COLORS = ['amber', 'green', 'cyan', 'red', 'white'];
  // Pixels per second, so a long tape is not a fast one.
  var SPEEDS = { slow: 40, normal: 70, fast: 110 };
  // AUTO follows the system's reduced-motion setting; ON and OFF override it.
  var MOTIONS = ['auto', 'on', 'off'];

  function isString(v) { return typeof v === 'string'; }

  function read() {
    var stored = {};
    try {
      var raw = window.localStorage.getItem(STORAGE_KEY);
      if (raw) { stored = JSON.parse(raw) || {}; }
    } catch (e) {
      stored = {};
    }
    return {
      order: Array.isArray(stored.order) ? stored.order.filter(isString) : [],
      hidden: Array.isArray(stored.hidden) ? stored.hidden.filter(isString) : [],
      color: COLORS.indexOf(stored.color) >= 0 ? stored.color : 'amber',
      speed: Object.prototype.hasOwnProperty.call(SPEEDS, stored.speed) ? stored.speed : 'normal',
      motion: MOTIONS.indexOf(stored.motion) >= 0 ? stored.motion : 'auto'
    };
  }

  function write(prefs) {
    try {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
    } catch (e) {
      // Private mode or blocked storage: the change applies to this page only.
    }
  }

  function clear() {
    try {
      window.localStorage.removeItem(STORAGE_KEY);
    } catch (e) {
      // Nothing stored, nothing to clear.
    }
  }

  function still(prefs) {
    if (prefs.motion === 'on') { return false; }
    if (prefs.motion === 'off') { return true; }
    return !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  }

  // Applied to the root before the body is parsed, so the tape never flashes
  // amber or moving first.
  function applyRoot(prefs) {
    document.documentElement.setAttribute('data-tape-color', prefs.color);
    document.documentElement.setAttribute('data-tape-motion', still(prefs) ? 'still' : 'run');
  }
  applyRoot(read());

  // rank places a group by the stored order; a group the stored order has never
  // seen keeps its catalogue position, after every group it does know.
  function rank(order, key, catalogueIndex) {
    var i = order.indexOf(key);
    return i < 0 ? order.length + catalogueIndex : i;
  }

  function build(win) {
    var track = win.querySelector('[data-tape-track]');
    var run = win.querySelector('[data-tape-run]');
    var idle = win.querySelector('[data-tape-empty]');
    if (idle) { idle.remove(); }
    if (!track || !run) { return; }

    track.querySelectorAll('[data-tape-clone]').forEach(function (c) { c.remove(); });
    track.classList.remove('is-running');
    run.style.minWidth = '';

    var prefs = read();
    var items = Array.prototype.slice.call(run.children);
    var catalogue = {};
    items.forEach(function (li, i) {
      if (!li.hasAttribute('data-tape-index')) { li.setAttribute('data-tape-index', String(i)); }
      var group = li.getAttribute('data-tape-group');
      if (!Object.prototype.hasOwnProperty.call(catalogue, group)) {
        catalogue[group] = Object.keys(catalogue).length;
      }
    });

    items.sort(function (a, b) {
      var ga = a.getAttribute('data-tape-group');
      var gb = b.getAttribute('data-tape-group');
      return (rank(prefs.order, ga, catalogue[ga]) - rank(prefs.order, gb, catalogue[gb])) ||
        (Number(a.getAttribute('data-tape-index')) - Number(b.getAttribute('data-tape-index')));
    });

    var visible = 0;
    items.forEach(function (li) {
      li.hidden = prefs.hidden.indexOf(li.getAttribute('data-tape-group')) >= 0;
      if (!li.hidden) { visible++; }
      run.appendChild(li);
    });

    track.hidden = visible === 0;
    if (visible === 0) {
      var p = document.createElement('p');
      p.className = 'tape-idle';
      p.setAttribute('data-tape-empty', '');
      p.textContent = '— TODOS LOS INDICADORES ESTÁN APAGADOS —';
      win.appendChild(p);
      return;
    }
    if (still(prefs)) { return; }

    // Two identical runs, each at least as wide as the window, scrolled by
    // half the track: the loop has no seam and no gap.
    run.style.minWidth = win.clientWidth + 'px';
    var clone = run.cloneNode(true);
    clone.removeAttribute('data-tape-run');
    clone.setAttribute('data-tape-clone', '');
    clone.setAttribute('aria-hidden', 'true');
    clone.querySelectorAll('a').forEach(function (a) { a.tabIndex = -1; });
    track.appendChild(clone);

    var seconds = run.getBoundingClientRect().width / SPEEDS[prefs.speed];
    track.style.setProperty('--tape-duration', Math.max(seconds, 8).toFixed(1) + 's');
    void track.offsetWidth; // restart the animation from the start
    track.classList.add('is-running');
  }

  function buildAll() {
    applyRoot(read());
    document.querySelectorAll('[data-tape-window]').forEach(build);
  }

  document.addEventListener('htmx:afterSwap', function (e) {
    var target = e.detail && e.detail.target;
    if (target && target.matches && target.matches('[data-tape-window]')) { build(target); }
  });

  var resizeTimer = null;
  window.addEventListener('resize', function () {
    window.clearTimeout(resizeTimer);
    resizeTimer = window.setTimeout(buildAll, 200);
  });

  window.FTTicker = {
    read: read,
    write: write,
    clear: clear,
    apply: buildAll,
    colors: COLORS.slice(),
    speeds: Object.keys(SPEEDS),
    motions: MOTIONS.slice()
  };
})();
