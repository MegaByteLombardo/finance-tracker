// The currency exchange form: restrict the destination to a different currency,
// and preview the implied rate.
//
// Everything here is a CONVENIENCE. The backend validates both accounts, reads
// both amounts at their own currency's scale, and derives the rate it records;
// nothing on this page decides anything. In particular the preview below is
// allowed to use ordinary JavaScript arithmetic precisely because it is never
// stored, never posted, and never compared with what the ledger computes -- the
// exact value comes back from the server after saving.
(function () {
  'use strict';

  const source = document.getElementById('source_account_id');
  const destination = document.getElementById('destination_account_id');
  const sourceAmount = document.getElementById('source_amount');
  const destinationAmount = document.getElementById('destination_amount');
  const sourceBadge = document.getElementById('source-currency-badge');
  const destinationBadge = document.getElementById('destination-currency-badge');
  const impliedRate = document.getElementById('implied-rate');

  if (!source || !destination) return;

  function currencyOf(select) {
    const option = select.options[select.selectedIndex];
    return option ? option.getAttribute('data-currency') : null;
  }

  // The destination must be a different account AND a different currency. This
  // mirrors the backend's rule rather than replacing it: a disabled option is a
  // hint, and the server refuses the same combinations whatever the browser did.
  function restrictDestination() {
    const sourceCurrency = currencyOf(source);
    const sourceId = source.value;

    if (sourceBadge) sourceBadge.textContent = sourceCurrency || '';

    for (let i = 0; i < destination.options.length; i++) {
      const option = destination.options[i];
      if (!option.value) continue;

      const sameAccount = option.value === sourceId;
      const sameCurrency = Boolean(sourceCurrency) &&
        option.getAttribute('data-currency') === sourceCurrency;

      option.disabled = sameAccount || sameCurrency;
      if (option.selected && option.disabled) destination.value = '';
    }

    if (destinationBadge) destinationBadge.textContent = currencyOf(destination) || '';
  }

  // amountOf reads a field the currency formatter has grouped: the comma there
  // only ever separates thousands.
  function amountOf(input) {
    return Number(input ? input.value.replace(/,/g, '') : NaN);
  }

  function updateImpliedRate() {
    if (!impliedRate) return;

    const sourceCurrency = currencyOf(source);
    const destinationCurrency = currencyOf(destination);
    const delivered = amountOf(sourceAmount);
    const received = amountOf(destinationAmount);

    if (!sourceCurrency || !destinationCurrency ||
        !isFinite(delivered) || !isFinite(received) || delivered <= 0 || received <= 0) {
      impliedRate.textContent = 'Enter both amounts to see the rate';
      return;
    }

    // The same orientation rule the backend applies: USD is the base if one side
    // is USD, otherwise whichever side makes the rate at least 1.
    let base, quote, value;
    const perDestination = delivered / received;
    if (sourceCurrency === 'USD') {
      base = sourceCurrency; quote = destinationCurrency; value = received / delivered;
    } else if (destinationCurrency === 'USD' || perDestination >= 1) {
      base = destinationCurrency; quote = sourceCurrency; value = perDestination;
    } else {
      base = sourceCurrency; quote = destinationCurrency; value = received / delivered;
    }

    // Six places is a preview, not a statement. The recorded rate is exact.
    const shown = Number(value.toFixed(6)).toString();
    impliedRate.textContent = '≈ 1 ' + base + ' = ' + shown + ' ' + quote;
  }

  function refresh() {
    restrictDestination();
    updateImpliedRate();
  }

  source.addEventListener('change', refresh);
  destination.addEventListener('change', refresh);
  if (sourceAmount) sourceAmount.addEventListener('input', updateImpliedRate);
  if (destinationAmount) destinationAmount.addEventListener('input', updateImpliedRate);

  refresh();
})();
