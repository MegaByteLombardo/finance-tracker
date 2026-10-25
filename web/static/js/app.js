// Finance Tracker - Presentation JavaScript
//
// Auto-formatting for currency amount inputs. Groups thousands for display and
// pads the decimals, then submits the CANONICAL format the backend expects --
// digits, one optional decimal point, no separators. See docs/m3-contract.md.
//
// The number of decimals comes from the currency's minor unit, never from a
// constant: JPY has 0 and KWD has 3, and a form that assumed 2 would silently
// mangle either.

(function () {
  'use strict';

  const DEFAULT_MINOR_UNIT = 2;

  // scaleFor reads the decimals the selected currency actually has.
  //
  // The currency box is checked first because it is what htmx replaces when the
  // account changes, so it is current even when the input's own attribute was
  // rendered for a different account.
  function scaleFor(input) {
    const box = document.getElementById('currency-display-box');
    const raw = (box && box.dataset.minorUnit) || (input && input.dataset.minorUnit);
    const n = parseInt(raw, 10);
    if (Number.isInteger(n) && n >= 0 && n <= 4) {
      return n;
    }
    return DEFAULT_MINOR_UNIT;
  }

  function formatNumberParts(rawVal, scale) {
    if (!rawVal) return '';

    let isNegative = false;
    let s = rawVal.trim();
    if (s.startsWith('-')) {
      isNegative = true;
      s = s.substring(1);
    }

    // A comma with no dot present is a decimal comma, as long as what follows
    // could be a fraction at this currency's scale.
    if (s.includes(',') && !s.includes('.') && scale > 0) {
      let commaIdx = s.lastIndexOf(',');
      let afterComma = s.substring(commaIdx + 1);
      if (afterComma.length <= scale) {
        s = s.substring(0, commaIdx) + '.' + afterComma;
      }
    }

    let parts = s.split('.');
    let intDigits = parts[0].replace(/\D/g, '');
    let decDigits = (scale > 0 && parts.length > 1)
      ? parts[1].replace(/\D/g, '').slice(0, scale)
      : null;

    if (!intDigits && decDigits === null) {
      return isNegative ? '-' : '';
    }

    let formattedInt = intDigits.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
    if (!formattedInt && decDigits !== null) {
      formattedInt = '0';
    }

    let res = (isNegative ? '-' : '') + formattedInt;
    if (decDigits !== null) {
      res += '.' + decDigits;
    } else if (scale > 0 && s.endsWith('.')) {
      res += '.';
    }
    return res;
  }

  function handleCurrencyInput(e) {
    const input = e.target;
    const oldVal = input.value;
    const oldCursor = input.selectionStart || 0;

    const beforeCursor = oldVal.slice(0, oldCursor);
    const digitsBefore = (beforeCursor.match(/[\d.]/g) || []).length;

    const newVal = formatNumberParts(oldVal, scaleFor(input));
    input.value = newVal;

    let newCursor = 0;
    let counted = 0;
    for (let i = 0; i < newVal.length; i++) {
      if (/[\d.]/.test(newVal[i])) {
        counted++;
      }
      if (counted === digitsBefore) {
        newCursor = i + 1;
        break;
      }
    }
    if (digitsBefore === 0) {
      newCursor = newVal.startsWith('-') ? 1 : 0;
    }
    input.setSelectionRange(newCursor, newCursor);
  }

  function handleCurrencyBlur(e) {
    const input = e.target;
    const scale = scaleFor(input);
    let val = input.value.trim();
    if (!val || val === '-') return;

    let isNegative = val.startsWith('-');
    let clean = val.replace(/^-/, '').replace(/,/g, '');

    if (!clean) {
      input.value = '';
      return;
    }

    let parts = clean.split('.');
    let intPart = parts[0].replace(/\D/g, '') || '0';
    let decPart = parts.length > 1 ? parts[1].replace(/\D/g, '') : '';

    let formattedInt = intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
    let res = (isNegative ? '-' : '') + formattedInt;

    // A currency with no minor unit gets no decimal point at all.
    if (scale > 0) {
      res += '.' + (decPart + '0'.repeat(scale)).slice(0, scale);
    }
    input.value = res;
  }

  // canonicalise rewrites the field to what the backend accepts, immediately
  // before the form is submitted.
  //
  // The backend deliberately does NOT strip separators: doing that blindly turns
  // "1,5" into "15". Producing the canonical format here is the presentation
  // layer's job, and it is the only place that knows which convention it just
  // displayed.
  function canonicaliseOnSubmit(form) {
    if (form._currencyCanonInit) return;
    form._currencyCanonInit = true;

    form.addEventListener('submit', () => {
      form.querySelectorAll('input[data-type="currency"]').forEach(input => {
        const val = input.value.trim();
        if (val) {
          input.value = val.replace(/,/g, '');
        }
      });
    });
  }

  // An htmx request that includes a form -- the savings hint under a transfer
  // or an expense, sent while the user types -- carries the amount fields too,
  // and must carry them canonical like a submission does. The conversion lives
  // here, where the grouping was added, never in the backend (m3-contract §4).
  document.addEventListener('htmx:configRequest', (e) => {
    const formData = e.detail.formData;
    const form = e.target && e.target.closest && e.target.closest('form');
    if (!formData || !form) return;
    form.querySelectorAll('input[data-type="currency"]').forEach(input => {
      if (input.name && formData.has(input.name)) {
        formData.set(input.name, input.value.trim().replace(/,/g, ''));
      }
    });
  });

  // followCurrencySelect keeps an amount at the scale of the currency chosen in
  // the select its data-minor-unit-from names -- the card's currency, or the
  // account an amount leaves or enters -- whose options carry data-minor-unit.
  // An exchange has two amounts in two currencies, so each follows its own.
  //
  // A value with more decimals than the new scale is left as typed rather than
  // reformatted: reformatting would drop digits silently, and the backend
  // refuses the extra decimals with a message instead.
  function followCurrencySelect(input) {
    const select = document.getElementById(input.dataset.minorUnitFrom);
    if (!select) return;

    function apply() {
      const option = select.options[select.selectedIndex];
      if (!option || option.dataset.minorUnit === undefined) return;
      input.dataset.minorUnit = option.dataset.minorUnit;
      const decimals = (input.value.split('.')[1] || '').length;
      if (input.value && decimals <= scaleFor(input)) {
        handleCurrencyBlur({ target: input });
      }
    }

    select.addEventListener('change', apply);
    apply();
  }

  function initCurrencyInputs(root) {
    const container = root || document;
    const inputs = container.querySelectorAll('input[data-type="currency"]');
    inputs.forEach(input => {
      if (input._currencyInit) return;
      input._currencyInit = true;

      input.addEventListener('input', handleCurrencyInput);
      input.addEventListener('blur', handleCurrencyBlur);

      if (input.form) {
        canonicaliseOnSubmit(input.form);
      }
      if (input.dataset.minorUnitFrom) {
        followCurrencySelect(input);
      }

      if (input.value) {
        handleCurrencyBlur({ target: input });
      }
    });
  }

  // Labels for the account-type filter. A row's key is its type, refined by
  // its subtype for banks; a key with no label here shows as it is stored.
  const ACCOUNT_TYPE_LABELS = {
    BANK: 'Banco',
    BANK_CHECKING: 'Banco: Corriente',
    BANK_SAVINGS: 'Banco: Ahorro',
    CASH: 'Efectivo',
    WALLET: 'Billetera digital',
    CREDIT_CARD: 'Tarjeta de crédito',
    OTHER: 'Otro',
  };

  function accountTypeKey(row) {
    const type = (row.getAttribute('data-account-type') || '').toUpperCase();
    const subtype = (row.getAttribute('data-account-subtype') || '').toUpperCase();
    return type === 'BANK' && subtype ? `BANK_${subtype}` : type;
  }

  // initMultiSelect turns a .tx-ms shell into a checkbox dropdown. values are
  // the keys found in the rows; '' stands for rows without one and is listed
  // last as emptyLabel. Nothing checked means the filter is off.
  function initMultiSelect(root, values, labelFor, emptyLabel, onChange) {
    const trigger = root.querySelector('.tx-ms-trigger');
    const text = root.querySelector('.tx-ms-text');
    const badge = root.querySelector('.tx-ms-badge');
    const panel = root.querySelector('.tx-ms-panel');
    const list = root.querySelector('.tx-ms-options');
    const baseLabel = root.dataset.label || '';

    const sorted = values.filter(v => v !== '').sort((a, b) => labelFor(a).localeCompare(labelFor(b)));
    if (values.includes('')) sorted.push('');

    const boxes = sorted.map(value => {
      const item = document.createElement('label');
      item.className = 'tx-ms-option' + (value === '' ? ' is-empty' : '');
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.value = value;
      const span = document.createElement('span');
      span.textContent = value === '' ? emptyLabel : labelFor(value);
      item.append(box, span);
      list.appendChild(item);
      box.addEventListener('change', () => { refresh(); onChange(); });
      return box;
    });

    if (boxes.length === 0) trigger.disabled = true;

    function refresh() {
      const picked = boxes.filter(b => b.checked);
      root.classList.toggle('is-active', picked.length > 0);
      if (picked.length === 1) {
        text.textContent = picked[0].nextSibling.textContent;
        badge.hidden = true;
      } else {
        text.textContent = baseLabel;
        badge.hidden = picked.length === 0;
        badge.textContent = picked.length === boxes.length ? 'todas' : String(picked.length);
      }
    }

    function setAll(checked) {
      boxes.forEach(b => { b.checked = checked; });
      refresh();
    }

    function open(state) {
      panel.hidden = !state;
      root.classList.toggle('is-open', state);
      trigger.setAttribute('aria-expanded', state ? 'true' : 'false');
    }

    trigger.addEventListener('click', () => {
      const willOpen = panel.hidden;
      document.querySelectorAll('.tx-ms.is-open').forEach(other => {
        if (other !== root) other._msOpen(false);
      });
      open(willOpen);
    });
    root.querySelector('[data-ms-all]').addEventListener('click', () => { setAll(true); onChange(); });
    root.querySelector('[data-ms-none]').addEventListener('click', () => { setAll(false); onChange(); });

    root._msOpen = open;
    refresh();

    return {
      selected: () => boxes.filter(b => b.checked).map(b => b.value),
      clear: () => setAll(false),
      // select checks every value accepted by match, for deep links.
      select: match => {
        boxes.forEach(b => { if (match(b.value)) b.checked = true; });
        refresh();
      },
    };
  }

  function initTransactionFilters() {
    const filterCard = document.getElementById('tx-filter-card');
    const table = document.querySelector('.data-table');
    if (!filterCard || !table || filterCard._txFilterInit) return;
    filterCard._txFilterInit = true;

    const rows = Array.from(table.querySelectorAll('tbody tr.tx-row'));
    if (rows.length === 0) return;

    const dateFromInput = document.getElementById('filter-date-from');
    const dateToInput = document.getElementById('filter-date-to');
    const searchInput = document.getElementById('filter-search-text');
    const resetBtn = document.getElementById('tx-filter-reset-btn');
    const emptyResetBtn = document.getElementById('tx-filter-empty-reset-btn');
    const countSpan = document.getElementById('tx-filter-count');
    const emptyRow = document.getElementById('tx-filter-empty-row');

    const attr = (row, name) => (row.getAttribute(name) || '').trim();

    // Each multi-select filter: the keys a row offers and how a key is shown.
    // The account filter matches a transfer's source or its destination.
    const specs = [
      { id: 'filter-institution', param: 'institution', empty: 'Sin institución',
        keys: row => [attr(row, 'data-institution')] },
      { id: 'filter-account-name', param: 'account', empty: 'Sin cuenta',
        keys: row => {
          const dest = attr(row, 'data-dest-account-name');
          return dest ? [attr(row, 'data-account-name'), dest] : [attr(row, 'data-account-name')];
        } },
      { id: 'filter-account-type', param: 'account_type', empty: 'Sin tipo',
        keys: row => [accountTypeKey(row)],
        label: v => ACCOUNT_TYPE_LABELS[v] || v,
        // ?account_type=BANK selects every kind of bank account.
        match: (v, want) => v === want || v.startsWith(`${want}_`) },
      { id: 'filter-category', param: 'category', empty: 'Sin categoría',
        keys: row => [attr(row, 'data-category')] },
      { id: 'filter-payment-method', param: 'payment_method', empty: 'Sin método',
        keys: row => [attr(row, 'data-payment-method')] },
    ];

    const filters = [];
    specs.forEach(spec => {
      const root = document.getElementById(spec.id);
      if (!root) return;
      const rowKeys = rows.map(spec.keys);
      const values = Array.from(new Set(rowKeys.flat()));
      const ms = initMultiSelect(root, values, spec.label || (v => v), spec.empty, applyFilters);
      filters.push({ spec, ms, rowKeys });
    });

    function applyFilters() {
      const dateFrom = (dateFromInput && dateFromInput.value) || '';
      const dateTo = (dateToInput && dateToInput.value) || '';
      const search = (searchInput && searchInput.value.trim().toLowerCase()) || '';
      const active = filters
        .map(f => ({ rowKeys: f.rowKeys, picked: new Set(f.ms.selected()) }))
        .filter(a => a.picked.size > 0);

      let visibleCount = 0;

      rows.forEach((row, i) => {
        const rowDate = row.getAttribute('data-date') || '';
        // Dates compare as YYYY-MM-DD strings.
        let visible = !(dateFrom && rowDate < dateFrom) && !(dateTo && rowDate > dateTo);
        if (visible) {
          visible = active.every(a => a.rowKeys[i].some(k => a.picked.has(k)));
        }
        if (visible && search) {
          visible = (row.getAttribute('data-search') || '').toLowerCase().includes(search);
        }
        row.style.display = visible ? '' : 'none';
        if (visible) visibleCount++;
      });

      const hasActive = Boolean(dateFrom || dateTo || search || active.length > 0);

      if (resetBtn) {
        resetBtn.style.display = hasActive ? 'inline-flex' : 'none';
      }

      if (countSpan) {
        countSpan.textContent = hasActive
          ? `${visibleCount} de ${rows.length} transacciones`
          : `${rows.length} transacciones`;
      }

      if (emptyRow) {
        emptyRow.style.display = visibleCount === 0 ? '' : 'none';
      }
    }

    function resetFilters() {
      [dateFromInput, dateToInput].forEach(el => {
        if (el) {
          el.value = '';
          el.dispatchEvent(new Event('change', { bubbles: true }));
        }
      });
      filters.forEach(f => f.ms.clear());
      if (searchInput) searchInput.value = '';
      applyFilters();
    }

    [dateFromInput, dateToInput].forEach(el => {
      if (el) el.addEventListener('change', applyFilters);
    });

    if (searchInput) {
      searchInput.addEventListener('input', applyFilters);
    }

    if (resetBtn) {
      resetBtn.addEventListener('click', resetFilters);
    }
    if (emptyResetBtn) {
      emptyResetBtn.addEventListener('click', resetFilters);
    }

    // Close an open multi-select on a click outside it or on Escape. Capture
    // phase, because the custom date picker stops its clicks from bubbling.
    document.addEventListener('click', e => {
      document.querySelectorAll('.tx-ms.is-open').forEach(ms => {
        if (!ms.contains(e.target)) ms._msOpen(false);
      });
    }, true);
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        document.querySelectorAll('.tx-ms.is-open').forEach(ms => ms._msOpen(false));
      }
    });

    // Pre-populate filters from the query string. A multi-select parameter may
    // repeat (?category=A&category=B).
    try {
      const params = new URLSearchParams(window.location.search);
      if (params.has('from') && dateFromInput) {
        dateFromInput.value = params.get('from');
        dateFromInput.dispatchEvent(new Event('change', { bubbles: true }));
      }
      if (params.has('to') && dateToInput) {
        dateToInput.value = params.get('to');
        dateToInput.dispatchEvent(new Event('change', { bubbles: true }));
      }
      filters.forEach(({ spec, ms }) => {
        const wanted = params.getAll(spec.param);
        if (spec.param === 'account') wanted.push(...params.getAll('account_name'));
        if (wanted.length === 0) return;
        const match = spec.match || ((v, want) => v === want);
        ms.select(v => wanted.some(want => match(v, want)));
      });
      if (params.has('q') && searchInput) {
        searchInput.value = params.get('q');
      }
    } catch (e) {
      console.warn('transaction filters: ignoring the query string', e);
    }
    applyFilters();
  }

  document.addEventListener('DOMContentLoaded', () => {
    initCurrencyInputs(document);
    initTransactionFilters();
  });

  document.addEventListener('htmx:load', (e) => {
    initCurrencyInputs(e.detail.elt || document);
    initTransactionFilters();
  });
})();
