// Finance Tracker - Ticker settings (TD-096)
//
// The list is rendered by the server in catalogue order. This script puts it in
// the stored order, keeps the controls in step with localStorage, and asks the
// tape above to re-apply on every change: that tape is the preview.
(function () {
  'use strict';

  function init() {
    var root = document.querySelector('[data-tape-settings]');
    var list = root && root.querySelector('[data-tape-list]');
    if (!root || !list || !window.FTTicker) { return; }
    var ticker = window.FTTicker;

    function rows() { return Array.prototype.slice.call(list.querySelectorAll('[data-key]')); }

    function render() {
      var prefs = ticker.read();
      var catalogue = rows().map(function (row) { return row.getAttribute('data-key'); });
      rows().sort(function (a, b) {
        var ka = a.getAttribute('data-key'), kb = b.getAttribute('data-key');
        var ia = prefs.order.indexOf(ka), ib = prefs.order.indexOf(kb);
        if (ia < 0) { ia = prefs.order.length + catalogue.indexOf(ka); }
        if (ib < 0) { ib = prefs.order.length + catalogue.indexOf(kb); }
        return ia - ib;
      }).forEach(function (row) {
        row.querySelector('[data-tape-toggle]').checked = prefs.hidden.indexOf(row.getAttribute('data-key')) < 0;
        list.appendChild(row);
      });
      root.querySelectorAll('input[name="tape-color"]').forEach(function (r) { r.checked = r.value === prefs.color; });
      root.querySelectorAll('input[name="tape-speed"]').forEach(function (r) { r.checked = r.value === prefs.speed; });
      root.querySelectorAll('input[name="tape-motion"]').forEach(function (r) { r.checked = r.value === prefs.motion; });
      syncMoveKeys();
    }

    function syncMoveKeys() {
      var all = rows();
      all.forEach(function (row, i) {
        row.querySelector('[data-tape-move="up"]').disabled = i === 0;
        row.querySelector('[data-tape-move="down"]').disabled = i === all.length - 1;
      });
    }

    function checkedValue(name, fallback) {
      var r = root.querySelector('input[name="' + name + '"]:checked');
      return r ? r.value : fallback;
    }

    function save() {
      var all = rows();
      ticker.write({
        order: all.map(function (row) { return row.getAttribute('data-key'); }),
        hidden: all.filter(function (row) { return !row.querySelector('[data-tape-toggle]').checked; })
          .map(function (row) { return row.getAttribute('data-key'); }),
        color: checkedValue('tape-color', 'amber'),
        speed: checkedValue('tape-speed', 'normal'),
        motion: checkedValue('tape-motion', 'auto')
      });
      syncMoveKeys();
      ticker.apply();
    }

    root.addEventListener('change', save);

    root.addEventListener('click', function (e) {
      var move = e.target.closest('[data-tape-move]');
      if (move) {
        var row = move.closest('[data-key]');
        if (move.getAttribute('data-tape-move') === 'up' && row.previousElementSibling) {
          list.insertBefore(row, row.previousElementSibling);
        } else if (move.getAttribute('data-tape-move') === 'down' && row.nextElementSibling) {
          list.insertBefore(row.nextElementSibling, row);
        }
        save();
        move.focus();
        return;
      }
      if (e.target.closest('[data-tape-reset]')) {
        ticker.clear();
        render();
        ticker.apply();
      }
    });

    render();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
