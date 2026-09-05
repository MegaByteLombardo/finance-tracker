// Finance Tracker - Presentation JavaScript
// Auto-formatting for currency amount inputs (separates thousands and decimals)

(function () {
  'use strict';

  function formatNumberParts(rawVal) {
    if (!rawVal) return '';

    let isNegative = false;
    let s = rawVal.trim();
    if (s.startsWith('-')) {
      isNegative = true;
      s = s.substring(1);
    }

    // If user typed a comma with no dot present, treat it as decimal separator
    if (s.includes(',') && !s.includes('.')) {
      let commaIdx = s.lastIndexOf(',');
      let afterComma = s.substring(commaIdx + 1);
      if (afterComma.length <= 2) {
        s = s.substring(0, commaIdx) + '.' + afterComma;
      }
    }

    let parts = s.split('.');
    let intDigits = parts[0].replace(/\D/g, '');
    let decDigits = parts.length > 1 ? parts[1].replace(/\D/g, '').slice(0, 2) : null;

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
    } else if (s.endsWith('.')) {
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

    const newVal = formatNumberParts(oldVal);
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

    decPart = (decPart + '00').slice(0, 2);
    let formattedInt = intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',');

    input.value = (isNegative ? '-' : '') + formattedInt + '.' + decPart;
  }

  function initCurrencyInputs(root) {
    const container = root || document;
    const inputs = container.querySelectorAll('input[data-type="currency"]');
    inputs.forEach(input => {
      if (input._currencyInit) return;
      input._currencyInit = true;

      input.addEventListener('input', handleCurrencyInput);
      input.addEventListener('blur', handleCurrencyBlur);

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
