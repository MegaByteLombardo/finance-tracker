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

      if (input.value) {
        handleCurrencyBlur({ target: input });
      }
    });
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
    const institutionSelect = document.getElementById('filter-institution');
    const accountNameSelect = document.getElementById('filter-account-name');
    const accountTypeSelect = document.getElementById('filter-account-type');
    const categorySelect = document.getElementById('filter-category');
    const paymentMethodSelect = document.getElementById('filter-payment-method');
    const searchInput = document.getElementById('filter-search-text');
    const resetBtn = document.getElementById('tx-filter-reset-btn');
    const emptyResetBtn = document.getElementById('tx-filter-empty-reset-btn');
    const countSpan = document.getElementById('tx-filter-count');
    const emptyRow = document.getElementById('tx-filter-empty-row');

    // Populate dynamic Institution, Account Name, Category and Payment Method options from existing rows
    const institutions = new Set();
    const accountNames = new Set();
    const categories = new Set();
    const paymentMethods = new Set();

    rows.forEach(row => {
      const inst = (row.getAttribute('data-institution') || '').trim();
      if (inst) institutions.add(inst);

      const acctName = (row.getAttribute('data-account-name') || '').trim();
      if (acctName) accountNames.add(acctName);

      const destAcctName = (row.getAttribute('data-dest-account-name') || '').trim();
      if (destAcctName) accountNames.add(destAcctName);

      const cat = (row.getAttribute('data-category') || '').trim();
      if (cat) categories.add(cat);

      const pm = (row.getAttribute('data-payment-method') || '').trim();
      if (pm) paymentMethods.add(pm);
    });

    if (institutionSelect) {
      Array.from(institutions).sort((a, b) => a.localeCompare(b)).forEach(inst => {
        const opt = document.createElement('option');
        opt.value = inst;
        opt.textContent = inst;
        institutionSelect.appendChild(opt);
      });
    }

    if (accountNameSelect) {
      Array.from(accountNames).sort((a, b) => a.localeCompare(b)).forEach(name => {
        const opt = document.createElement('option');
        opt.value = name;
        opt.textContent = name;
        accountNameSelect.appendChild(opt);
      });
    }

    if (categorySelect) {
      Array.from(categories).sort((a, b) => a.localeCompare(b)).forEach(cat => {
        const opt = document.createElement('option');
        opt.value = cat;
        opt.textContent = cat;
        categorySelect.appendChild(opt);
      });
    }

    if (paymentMethodSelect) {
      Array.from(paymentMethods).sort((a, b) => a.localeCompare(b)).forEach(pm => {
        const opt = document.createElement('option');
        opt.value = pm;
        opt.textContent = pm;
        paymentMethodSelect.appendChild(opt);
      });
    }

    function applyFilters() {
      const dateFrom = (dateFromInput && dateFromInput.value) || '';
      const dateTo = (dateToInput && dateToInput.value) || '';
      const instFilter = (institutionSelect && institutionSelect.value.trim().toLowerCase()) || '';
      const acctNameFilter = (accountNameSelect && accountNameSelect.value.trim().toLowerCase()) || '';
      const acctType = (accountTypeSelect && accountTypeSelect.value) || '';
      const cat = (categorySelect && categorySelect.value.trim().toLowerCase()) || '';
      const pm = (paymentMethodSelect && paymentMethodSelect.value.trim().toLowerCase()) || '';
      const search = (searchInput && searchInput.value.trim().toLowerCase()) || '';

      let visibleCount = 0;

      rows.forEach(row => {
        const rowDate = row.getAttribute('data-date') || '';
        const rowInst = (row.getAttribute('data-institution') || '').trim().toLowerCase();
        const rowAcctName = (row.getAttribute('data-account-name') || '').trim().toLowerCase();
        const rowDestAcctName = (row.getAttribute('data-dest-account-name') || '').trim().toLowerCase();
        const rowAcctType = (row.getAttribute('data-account-type') || '').toUpperCase();
        const rowAcctSubtype = (row.getAttribute('data-account-subtype') || '').toUpperCase();
        const rowCat = (row.getAttribute('data-category') || '').trim().toLowerCase();
        const rowPm = (row.getAttribute('data-payment-method') || '').trim().toLowerCase();
        const rowSearch = (row.getAttribute('data-search') || '').toLowerCase();

        // 1. Date filter (YYYY-MM-DD string comparison)
        if (dateFrom && rowDate < dateFrom) {
          row.style.display = 'none';
          return;
        }
        if (dateTo && rowDate > dateTo) {
          row.style.display = 'none';
          return;
        }

        // 2. Institution filter
        if (instFilter && rowInst !== instFilter) {
          row.style.display = 'none';
          return;
        }

        // 3. Account Name filter (matches either source or destination account in transfers/exchanges)
        if (acctNameFilter && rowAcctName !== acctNameFilter && rowDestAcctName !== acctNameFilter) {
          row.style.display = 'none';
          return;
        }

        // 4. Account type filter
        if (acctType) {
          if (acctType === 'BANK') {
            if (rowAcctType !== 'BANK') {
              row.style.display = 'none';
              return;
            }
          } else if (acctType === 'BANK_CHECKING') {
            if (rowAcctType !== 'BANK' || rowAcctSubtype !== 'CHECKING') {
              row.style.display = 'none';
              return;
            }
          } else if (acctType === 'BANK_SAVINGS') {
            if (rowAcctType !== 'BANK' || rowAcctSubtype !== 'SAVINGS') {
              row.style.display = 'none';
              return;
            }
          } else if (rowAcctType !== acctType) {
            row.style.display = 'none';
            return;
          }
        }

        // 5. Category filter
        if (cat && rowCat !== cat) {
          row.style.display = 'none';
          return;
        }

        // 6. Payment method filter
        if (pm && rowPm !== pm) {
          row.style.display = 'none';
          return;
        }

        // 7. Search text filter
        if (search && !rowSearch.includes(search)) {
          row.style.display = 'none';
          return;
        }

        row.style.display = '';
        visibleCount++;
      });

      const hasActive = Boolean(dateFrom || dateTo || instFilter || acctNameFilter || acctType || cat || pm || search);

      if (resetBtn) {
        resetBtn.style.display = hasActive ? 'inline-flex' : 'none';
      }

      if (countSpan) {
        if (hasActive) {
          countSpan.textContent = `Mostrando ${visibleCount} de ${rows.length} transacciones`;
        } else {
          countSpan.textContent = `Mostrando ${rows.length} transacciones`;
        }
      }

      if (emptyRow) {
        emptyRow.style.display = visibleCount === 0 ? '' : 'none';
      }
    }

    function resetFilters() {
      [dateFromInput, dateToInput, institutionSelect, accountNameSelect, accountTypeSelect, categorySelect, paymentMethodSelect].forEach(el => {
        if (el) {
          el.value = '';
          el.dispatchEvent(new Event('change', { bubbles: true }));
        }
      });
      if (searchInput) searchInput.value = '';
      applyFilters();
    }

    // Attach listeners
    [dateFromInput, dateToInput, institutionSelect, accountNameSelect, accountTypeSelect, categorySelect, paymentMethodSelect].forEach(el => {
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

    // Pre-populate filters from URL query parameters if present
    try {
      const params = new URLSearchParams(window.location.search);
      let filterChanged = false;
      if (params.has('from') && dateFromInput) {
        dateFromInput.value = params.get('from');
        dateFromInput.dispatchEvent(new Event('change', { bubbles: true }));
        filterChanged = true;
      }
      if (params.has('to') && dateToInput) {
        dateToInput.value = params.get('to');
        dateToInput.dispatchEvent(new Event('change', { bubbles: true }));
        filterChanged = true;
      }
      if (params.has('institution') && institutionSelect) {
        institutionSelect.value = params.get('institution');
        institutionSelect.dispatchEvent(new Event('change', { bubbles: true }));
        filterChanged = true;
      }
      if ((params.has('account') || params.has('account_name')) && accountNameSelect) {
        accountNameSelect.value = params.get('account') || params.get('account_name');
        accountNameSelect.dispatchEvent(new Event('change', { bubbles: true }));
        filterChanged = true;
      }
      if (params.has('account_type') && accountTypeSelect) {
        accountTypeSelect.value = params.get('account_type');
        accountTypeSelect.dispatchEvent(new Event('change', { bubbles: true }));
        filterChanged = true;
      }
      if (params.has('category') && categorySelect) {
        categorySelect.value = params.get('category');
        categorySelect.dispatchEvent(new Event('change', { bubbles: true }));
        filterChanged = true;
      }
      if (params.has('payment_method') && paymentMethodSelect) {
        paymentMethodSelect.value = params.get('payment_method');
        paymentMethodSelect.dispatchEvent(new Event('change', { bubbles: true }));
        filterChanged = true;
      }
      if (params.has('q') && searchInput) {
        searchInput.value = params.get('q');
        filterChanged = true;
      }
      if (filterChanged) {
        applyFilters();
      }
    } catch (e) {
      // Ignore URL parsing errors
    }
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
