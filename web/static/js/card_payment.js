// The card payment form (M8.2): copy a suggested amount into the field, and
// show the second amount when the source is in another currency.
//
// Everything here is a CONVENIENCE. The server renders the form's shape for
// the account it was given and validates every field; a suggestion only fills
// the amount, it never submits the form, and nothing on this page computes a
// figure -- the suggested amounts arrive formatted from the backend.
(function () {
  'use strict';

  const form = document.getElementById('card-payment-form');
  if (!form) return;

  const amount = document.getElementById('amount');
  const source = document.getElementById('source_account_id');
  const sourceGroup = document.getElementById('source-amount-group');
  const sourceBadge = document.getElementById('source-currency-badge');
  const cardCurrency = form.getAttribute('data-card-currency');

  document.querySelectorAll('[data-fill-amount]').forEach(button => {
    button.addEventListener('click', () => {
      if (!amount) return;
      amount.value = button.getAttribute('data-fill-amount');
      amount.dispatchEvent(new Event('blur'));
      amount.dispatchEvent(new KeyboardEvent('keyup'));
      amount.focus();
    });
  });

  function syncSourceAmount() {
    if (!source || !sourceGroup) return;
    const option = source.options[source.selectedIndex];
    const currency = option ? option.getAttribute('data-currency') : null;
    sourceGroup.hidden = !currency || currency === cardCurrency;
    if (sourceBadge) sourceBadge.textContent = currency || '';
  }

  if (source) source.addEventListener('change', syncSourceAmount);
  syncSourceAmount();
})();
