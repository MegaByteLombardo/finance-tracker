// Finance Tracker - net worth chart tooltip.
//
// The chart is an SVG laid out on the server (internal/web/chart.go). Next to
// it the server embeds every day of the range as JSON: its position on the
// 1000-wide canvas and its date and total, ALREADY FORMATTED. This script only
// finds the day under the pointer and places a guide, a dot and a card there.
// It never computes, converts or formats a figure (docs/m6-contract.md).
//
// Listeners are delegated to the document, so a chart that htmx swaps in when
// the period changes works without re-initialising anything.

(function () {
  'use strict';

  const CANVAS_WIDTH = 1000;
  const CANVAS_HEIGHT = 240;
  const GAP_MESSAGE = 'Sin tasa de cambio: este día no se puede valorar';

  // Parsed data, per data element. A swapped-in chart has a new element, so a
  // stale entry is never reused and is collected with the old element.
  const cache = new WeakMap();
  // The day each plot is showing, for keyboard navigation.
  const current = new WeakMap();

  function parts(plot) {
    const data = plot.querySelector('.nw-chart-data');
    if (!data) return null;
    let days = cache.get(data);
    if (!days) {
      try {
        days = JSON.parse(data.textContent) || [];
      } catch (err) {
        console.error('net worth chart: unreadable tooltip data', err);
        days = [];
      }
      cache.set(data, days);
    }
    return {
      days: days,
      guide: plot.querySelector('.nw-chart-guide'),
      marker: plot.querySelector('.nw-chart-marker'),
      tooltip: plot.querySelector('.nw-chart-tooltip'),
    };
  }

  // nearest finds the day closest to a canvas x. Days are in order of x.
  function nearest(days, x) {
    let lo = 0;
    let hi = days.length - 1;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (days[mid].x < x) lo = mid + 1;
      else hi = mid;
    }
    if (lo > 0 && x - days[lo - 1].x <= days[lo].x - x) return lo - 1;
    return lo;
  }

  function show(plot, index) {
    const p = parts(plot);
    if (!p || p.days.length === 0 || !p.tooltip) return;
    index = Math.max(0, Math.min(p.days.length - 1, index));
    current.set(plot, index);
    const day = p.days[index];

    const width = plot.clientWidth;
    const height = plot.clientHeight;
    const left = (day.x / CANVAS_WIDTH) * width;

    p.guide.style.left = left + 'px';
    p.guide.hidden = false;

    p.tooltip.classList.toggle('is-gap', !day.ok);
    p.tooltip.querySelector('.nw-chart-tooltip-date').textContent = day.d;
    p.tooltip.querySelector('.nw-chart-tooltip-value').textContent = day.ok ? day.v : GAP_MESSAGE;
    p.tooltip.hidden = false;

    // A gap is never drawn as a point, so it gets no dot either.
    let anchorY = height / 2;
    if (day.ok) {
      anchorY = (day.y / CANVAS_HEIGHT) * height;
      p.marker.style.left = left + 'px';
      p.marker.style.top = anchorY + 'px';
      p.marker.hidden = false;
    } else {
      p.marker.hidden = true;
    }

    // Beside the guide, on whichever side has room; above the point when it
    // fits, otherwise below; always inside the plot.
    const tw = p.tooltip.offsetWidth;
    const th = p.tooltip.offsetHeight;
    const gap = 12;
    let tx = left + gap;
    if (tx + tw > width) tx = left - gap - tw;
    tx = Math.max(0, Math.min(width - tw, tx));
    let ty = anchorY - th - gap;
    if (ty < 0) ty = anchorY + gap;
    ty = Math.max(0, Math.min(height - th, ty));
    p.tooltip.style.transform = 'translate(' + Math.round(tx) + 'px, ' + Math.round(ty) + 'px)';
  }

  function hide(plot) {
    const p = parts(plot);
    if (!p) return;
    p.guide.hidden = true;
    p.marker.hidden = true;
    p.tooltip.hidden = true;
    current.delete(plot);
  }

  function showAtPointer(plot, event) {
    const p = parts(plot);
    if (!p || p.days.length === 0) return;
    const rect = plot.getBoundingClientRect();
    if (rect.width === 0) return;
    const x = ((event.clientX - rect.left) / rect.width) * CANVAS_WIDTH;
    show(plot, nearest(p.days, x));
  }

  // lastComplete is where the keyboard starts: today, or the latest day that
  // could be valued.
  function lastComplete(days) {
    for (let i = days.length - 1; i >= 0; i--) {
      if (days[i].ok) return i;
    }
    return days.length - 1;
  }

  document.addEventListener('pointermove', function (event) {
    const plot = event.target.closest && event.target.closest('.nw-chart-plot');
    if (plot) showAtPointer(plot, event);
  });

  document.addEventListener('pointerdown', function (event) {
    const plot = event.target.closest && event.target.closest('.nw-chart-plot');
    if (plot) {
      showAtPointer(plot, event);
      return;
    }
    // A tap elsewhere dismisses a tooltip left open by touch.
    document.querySelectorAll('.nw-chart-plot').forEach(hide);
  });

  // pointerleave does not bubble; listening in the capture phase still sees it.
  // A finger lifting off the screen also "leaves"; that tooltip stays until the
  // next tap elsewhere, so it can actually be read.
  document.addEventListener('pointerleave', function (event) {
    if (event.pointerType === 'touch') return;
    const target = event.target;
    if (target instanceof Element && target.classList.contains('nw-chart-plot')) hide(target);
  }, true);

  document.addEventListener('focusin', function (event) {
    const target = event.target;
    if (target instanceof Element && target.classList.contains('nw-chart-plot')) {
      const p = parts(target);
      if (p) show(target, lastComplete(p.days));
    }
  });

  document.addEventListener('focusout', function (event) {
    const target = event.target;
    if (target instanceof Element && target.classList.contains('nw-chart-plot')) hide(target);
  });

  document.addEventListener('keydown', function (event) {
    const plot = event.target;
    if (!(plot instanceof Element) || !plot.classList.contains('nw-chart-plot')) return;
    const p = parts(plot);
    if (!p || p.days.length === 0) return;
    const at = current.has(plot) ? current.get(plot) : lastComplete(p.days);
    let next = null;
    switch (event.key) {
      case 'ArrowLeft': next = at - 1; break;
      case 'ArrowRight': next = at + 1; break;
      case 'Home': next = 0; break;
      case 'End': next = p.days.length - 1; break;
      case 'Escape': hide(plot); return;
      default: return;
    }
    event.preventDefault();
    show(plot, next);
  });

  // A resize moves every point; the open tooltip follows.
  window.addEventListener('resize', function () {
    document.querySelectorAll('.nw-chart-plot').forEach(function (plot) {
      if (current.has(plot)) show(plot, current.get(plot));
    });
  });
})();
