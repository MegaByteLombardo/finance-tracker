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

  document.addEventListener('DOMContentLoaded', () => {
    initCurrencyInputs(document);
  });

  document.addEventListener('htmx:load', (e) => {
    initCurrencyInputs(e.detail.elt || document);
  });
})();
