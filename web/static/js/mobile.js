// Finance Tracker - Mobile Interactions & Bottom Sheet

document.addEventListener('DOMContentLoaded', () => {
  const fabBtn = document.getElementById('fab-new-operation');
  const sheet = document.getElementById('operation-sheet');
  const backdrop = document.getElementById('operation-sheet-backdrop');
  const closeBtn = document.getElementById('sheet-close-btn');
  const cancelBtn = document.getElementById('sheet-cancel-button');
  const dragHandle = document.getElementById('sheet-drag-handle');

  if (!fabBtn || !sheet || !backdrop) return;

  function openSheet() {
    sheet.classList.add('is-open');
    backdrop.classList.add('is-visible');
    sheet.setAttribute('aria-hidden', 'false');
    backdrop.setAttribute('aria-hidden', 'false');
    fabBtn.setAttribute('aria-expanded', 'true');
    document.body.style.overflow = 'hidden'; // prevent scroll behind sheet
  }

  function closeSheet() {
    sheet.classList.remove('is-open');
    backdrop.classList.remove('is-visible');
    sheet.setAttribute('aria-hidden', 'true');
    backdrop.setAttribute('aria-hidden', 'true');
    fabBtn.setAttribute('aria-expanded', 'false');
    document.body.style.overflow = '';
  }

  fabBtn.addEventListener('click', (e) => {
    e.preventDefault();
    openSheet();
  });

  if (closeBtn) {
    closeBtn.addEventListener('click', (e) => {
      e.preventDefault();
      closeSheet();
    });
  }

  if (cancelBtn) {
    cancelBtn.addEventListener('click', (e) => {
      e.preventDefault();
      closeSheet();
    });
  }

  backdrop.addEventListener('click', closeSheet);

  // Close on Escape key
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && sheet.classList.contains('is-open')) {
      closeSheet();
    }
  });

  // Touch / Drag down to dismiss sheet
  let startY = 0;
  let currentY = 0;
  let isDragging = false;

  function onTouchStart(e) {
    startY = e.touches[0].clientY;
    currentY = startY;
    isDragging = true;
    sheet.style.transition = 'none';
  }

  function onTouchMove(e) {
    if (!isDragging) return;
    currentY = e.touches[0].clientY;
    const deltaY = currentY - startY;
    if (deltaY > 0) {
      sheet.style.transform = `translateY(${deltaY}px)`;
    }
  }

  function onTouchEnd() {
    if (!isDragging) return;
    isDragging = false;
    sheet.style.transition = '';
    const deltaY = currentY - startY;
    if (deltaY > 80) {
      closeSheet();
      sheet.style.transform = '';
    } else {
      sheet.style.transform = '';
    }
  }

  if (dragHandle) {
    dragHandle.addEventListener('touchstart', onTouchStart, { passive: true });
    dragHandle.addEventListener('touchmove', onTouchMove, { passive: true });
    dragHandle.addEventListener('touchend', onTouchEnd, { passive: true });
  }

  // Handle operation option click
  const opButtons = sheet.querySelectorAll('.op-button');
  opButtons.forEach(btn => {
    btn.addEventListener('click', (e) => {
      const opType = btn.getAttribute('data-operation-type');
      if (opType === 'transfer') {
        // Conceptually transfer: if not hooked to backend yet, provide friendly feedback or follow link
        console.log('Operación seleccionada:', opType);
      }
    });
  });

  // ==========================================================================
  // Movimientos Feed: Search & Filter Logic
  // ==========================================================================
  const searchInput = document.getElementById('tx-search-input');
  const clearBtn = document.getElementById('tx-search-clear-btn');
  const filterChips = document.querySelectorAll('.filter-chip');
  const itemWrappers = document.querySelectorAll('.tx-feed-item-wrapper');
  const dateGroups = document.querySelectorAll('.tx-date-group');
  const emptyState = document.getElementById('tx-empty-filter-state');
  const resetFiltersBtn = document.getElementById('tx-reset-filters-btn');

  if (itemWrappers.length > 0) {
    let currentFilter = 'all';
    let currentQuery = '';

    function applyFilters() {
      let visibleCount = 0;

      itemWrappers.forEach(item => {
        const type = item.getAttribute('data-tx-type') || '';
        const currency = item.getAttribute('data-tx-currency') || '';
        const merchant = item.getAttribute('data-tx-merchant') || '';
        const category = item.getAttribute('data-tx-category') || '';
        const account = item.getAttribute('data-tx-account') || '';

        // Match chip filter
        let matchFilter = false;
        if (currentFilter === 'all') {
          matchFilter = true;
        } else if (currentFilter === 'expense' && type === 'expense') {
          matchFilter = true;
        } else if (currentFilter === 'income' && type === 'income') {
          matchFilter = true;
        } else if (currentFilter === 'transfer' && type === 'transfer') {
          matchFilter = true;
        } else if (currentFilter === 'fx_exchange' && type === 'fx_exchange') {
          matchFilter = true;
        } else if (currentFilter === 'usd' && currency === 'usd') {
          matchFilter = true;
        } else if (currentFilter === 'ves' && currency === 'ves') {
          matchFilter = true;
        }

        // Match search query
        let matchQuery = true;
        if (currentQuery) {
          const haystack = (merchant + ' ' + category + ' ' + account).toLowerCase();
          matchQuery = haystack.includes(currentQuery);
        }

        if (matchFilter && matchQuery) {
          item.classList.remove('is-hidden');
          visibleCount++;
        } else {
          item.classList.add('is-hidden');
        }
      });

      // Update date groups visibility: hide group if all its items are hidden
      dateGroups.forEach(group => {
        const groupItems = group.querySelectorAll('.tx-feed-item-wrapper');
        const hasVisible = Array.from(groupItems).some(item => !item.classList.contains('is-hidden'));
        if (hasVisible) {
          group.classList.remove('is-hidden');
        } else {
          group.classList.add('is-hidden');
        }
      });

      // Toggle empty state
      if (emptyState) {
        emptyState.style.display = visibleCount === 0 ? 'flex' : 'none';
      }
    }

    if (searchInput) {
      searchInput.addEventListener('input', (e) => {
        currentQuery = e.target.value.trim().toLowerCase();
        if (clearBtn) {
          clearBtn.style.display = currentQuery ? 'flex' : 'none';
        }
        applyFilters();
      });
    }

    if (clearBtn && searchInput) {
      clearBtn.addEventListener('click', () => {
        searchInput.value = '';
        currentQuery = '';
        clearBtn.style.display = 'none';
        searchInput.focus();
        applyFilters();
      });
    }

    filterChips.forEach(chip => {
      chip.addEventListener('click', () => {
        filterChips.forEach(c => {
          c.classList.remove('is-active');
          c.setAttribute('aria-selected', 'false');
        });
        chip.classList.add('is-active');
        chip.setAttribute('aria-selected', 'true');
        currentFilter = chip.getAttribute('data-filter') || 'all';
        applyFilters();
      });
    });

    if (resetFiltersBtn) {
      resetFiltersBtn.addEventListener('click', () => {
        if (searchInput) {
          searchInput.value = '';
          currentQuery = '';
          if (clearBtn) clearBtn.style.display = 'none';
        }
        filterChips.forEach(c => {
          const isAll = c.getAttribute('data-filter') === 'all';
          c.classList.toggle('is-active', isAll);
          c.setAttribute('aria-selected', isAll ? 'true' : 'false');
        });
        currentFilter = 'all';
        applyFilters();
      });
    }
  }

  // ==========================================================================
  // New Operation Form: Type Switching & Category Selection
  // ==========================================================================
  const opTypeTabs = document.querySelectorAll('.op-type-tab');
  const opInputType = document.getElementById('op-input-type');
  const opSubmitBtn = document.getElementById('op-submit-button');
  const opForm = document.getElementById('mobile-operation-form');
  const opAmountInput = document.getElementById('op-amount-input');
  const opAmountLabel = document.getElementById('op-amount-label');
  const categoryGroup = document.getElementById('op-category-group');
  const destinationGroup = document.getElementById('op-destination-group');
  const destinationAmountGroup = document.getElementById('op-destination-amount-group');
  const destinationHelp = document.getElementById('op-destination-help');
  const destinationAmount = document.getElementById('op-destination-amount');
  const impliedRate = document.getElementById('op-implied-rate');
  const methodGroup = document.getElementById('op-method-group');
  const labelOriginAccount = document.getElementById('label-origin-account');
  const srcSelect = document.getElementById('op-account-select');
  const dstSelect = document.getElementById('op-dest-account-select');
  const catChips = document.querySelectorAll('.op-cat-chip');

  // The four shapes this one form can take. It is the same table the server
  // renders the initial state from (MobileOperationShape in Go), restated here
  // because switching tabs must not need a round trip. A test in internal/web
  // asserts the two agree.
  const OPERATION_SHAPES = {
    EXPENSE: {
      action: '/transactions', sourceField: 'account_id', amountField: 'amount',
      originLabel: 'Cuenta', amountLabel: 'Monto de la operación',
      submitLabel: 'Registrar Gasto', submitClass: 'btn-expense', tabClass: 'type-expense',
      category: true, method: true, destination: false, destinationAmount: false
    },
    INCOME: {
      action: '/transactions', sourceField: 'account_id', amountField: 'amount',
      originLabel: 'Cuenta de abono', amountLabel: 'Monto de la operación',
      submitLabel: 'Registrar Ingreso', submitClass: 'btn-income', tabClass: 'type-income',
      category: true, method: true, destination: false, destinationAmount: false
    },
    TRANSFER: {
      action: '/transfers', sourceField: 'source_account_id', amountField: 'amount',
      originLabel: 'Cuenta de origen', amountLabel: 'Monto de la operación',
      submitLabel: 'Registrar Transferencia', submitClass: 'btn-transfer', tabClass: 'type-transfer',
      category: false, method: false, destination: true, destinationAmount: false
    },
    FX_EXCHANGE: {
      action: '/exchanges', sourceField: 'source_account_id', amountField: 'source_amount',
      originLabel: 'Cuenta de origen (entregado)', amountLabel: 'Monto entregado',
      submitLabel: 'Registrar Cambio', submitClass: 'btn-exchange', tabClass: 'type-exchange',
      category: false, method: false, destination: true, destinationAmount: true
    }
  };

  function filterCategoriesForType(targetType) {
    let firstSelected = false;
    catChips.forEach(chip => {
      const chipType = chip.getAttribute('data-cat-type');
      const match = (chipType === targetType);
      chip.style.display = match ? 'inline-flex' : 'none';
      const radio = chip.querySelector('input[type="radio"]');
      if (match && !firstSelected) {
        chip.classList.add('is-selected');
        if (radio) radio.checked = true;
        firstSelected = true;
      } else {
        chip.classList.remove('is-selected');
        if (radio) radio.checked = false;
      }
    });
  }

  function currencyOf(select) {
    if (!select) return null;
    const option = select.options[select.selectedIndex];
    return option ? option.getAttribute('data-currency') : null;
  }

  // The destination is restricted to the accounts the chosen operation can
  // actually reach: the SAME currency for a transfer, a DIFFERENT one for an
  // exchange. This mirrors the rule the backend enforces rather than replacing
  // it -- a disabled option is a hint, and the server refuses the same
  // combinations whatever the browser did.
  function restrictDestination(type) {
    if (!srcSelect || !dstSelect) return;

    const sourceCurrency = currencyOf(srcSelect);
    const sourceId = srcSelect.value;

    for (let i = 0; i < dstSelect.options.length; i++) {
      const option = dstSelect.options[i];
      if (!option.value) continue;

      const optionCurrency = option.getAttribute('data-currency');
      const sameAccount = option.value === sourceId;
      let wrongCurrency = false;
      if (sourceCurrency) {
        wrongCurrency = (type === 'FX_EXCHANGE')
          ? optionCurrency === sourceCurrency
          : optionCurrency !== sourceCurrency;
      }

      option.disabled = Boolean(sourceId) && (sameAccount || wrongCurrency);
      if (option.selected && option.disabled) dstSelect.value = '';
    }
  }

  // A preview of the implied rate, while the user types.
  //
  // It is allowed to use ordinary JavaScript arithmetic precisely because it is
  // never stored, never posted and never compared with what the ledger derives:
  // the exact rate comes back from the server after saving.
  function updateImpliedRate() {
    if (!impliedRate) return;

    const sourceCurrency = currencyOf(srcSelect);
    const destinationCurrency = currencyOf(dstSelect);
    const delivered = Number(opAmountInput && opAmountInput.value);
    const received = Number(destinationAmount && destinationAmount.value);

    if (!sourceCurrency || !destinationCurrency ||
        !isFinite(delivered) || !isFinite(received) || delivered <= 0 || received <= 0) {
      impliedRate.textContent = 'Introduce ambos montos para ver la tasa implícita.';
      return;
    }

    // The same orientation rule the backend applies: USD is the base if one
    // side is USD, otherwise whichever side makes the rate at least 1.
    let base, quote, value;
    const perDestination = delivered / received;
    if (sourceCurrency === 'USD') {
      base = sourceCurrency; quote = destinationCurrency; value = received / delivered;
    } else if (destinationCurrency === 'USD' || perDestination >= 1) {
      base = destinationCurrency; quote = sourceCurrency; value = perDestination;
    } else {
      base = sourceCurrency; quote = destinationCurrency; value = received / delivered;
    }

    impliedRate.textContent = 'Tasa implícita ≈ 1 ' + base + ' = ' +
      Number(value.toFixed(6)).toString() + ' ' + quote;
  }

  // applyOperationType puts the form into the shape one operation needs.
  //
  // It runs on every tab click AND once on load. Running it on load is not
  // decoration: before M5.5 it ran only on click, so a form rendered for
  // ?type=TRANSFER kept the income endpoint and the income field names until the
  // user happened to press a tab.
  function applyOperationType(type) {
    const shape = OPERATION_SHAPES[type] || OPERATION_SHAPES.EXPENSE;

    opTypeTabs.forEach(t => {
      const isActive = t.getAttribute('data-type') === type;
      t.classList.remove('is-active', 'type-expense', 'type-income', 'type-transfer', 'type-exchange');
      t.setAttribute('aria-selected', isActive ? 'true' : 'false');
      if (isActive) t.classList.add('is-active', shape.tabClass);
    });

    if (opInputType) opInputType.value = type;
    if (opForm) opForm.action = shape.action;
    if (srcSelect) srcSelect.name = shape.sourceField;
    if (opAmountInput) opAmountInput.name = shape.amountField;
    if (opAmountLabel) opAmountLabel.textContent = shape.amountLabel;
    if (labelOriginAccount) labelOriginAccount.textContent = shape.originLabel;

    if (opSubmitBtn) {
      opSubmitBtn.textContent = shape.submitLabel;
      opSubmitBtn.className = 'op-submit-btn ' + shape.submitClass;
    }

    if (categoryGroup) categoryGroup.style.display = shape.category ? 'flex' : 'none';
    if (methodGroup) methodGroup.style.display = shape.method ? 'flex' : 'none';
    if (destinationGroup) destinationGroup.style.display = shape.destination ? 'flex' : 'none';
    if (destinationAmountGroup) {
      destinationAmountGroup.style.display = shape.destinationAmount ? 'flex' : 'none';
    }
    // A hidden required field blocks submission in every browser, so the second
    // amount is required only while it is on screen.
    if (destinationAmount) destinationAmount.required = shape.destinationAmount;
    if (destinationHelp) destinationHelp.style.display = shape.destinationAmount ? 'block' : 'none';

    if (shape.category) filterCategoriesForType(type);
    restrictDestination(type);
    if (shape.destinationAmount) updateImpliedRate();
  }

  function currentOperationType() {
    return (opInputType && opInputType.value) || 'EXPENSE';
  }

  if (opTypeTabs.length > 0) {
    applyOperationType(currentOperationType());

    opTypeTabs.forEach(tab => {
      tab.addEventListener('click', () => {
        applyOperationType(tab.getAttribute('data-type'));
      });
    });

    if (srcSelect) {
      srcSelect.addEventListener('change', () => {
        restrictDestination(currentOperationType());
        updateImpliedRate();
      });
    }
    if (dstSelect) dstSelect.addEventListener('change', updateImpliedRate);
    if (opAmountInput) opAmountInput.addEventListener('input', updateImpliedRate);
    if (destinationAmount) destinationAmount.addEventListener('input', updateImpliedRate);

    catChips.forEach(chip => {
      chip.addEventListener('click', () => {
        catChips.forEach(c => c.classList.remove('is-selected'));
        chip.classList.add('is-selected');
        const radio = chip.querySelector('input[type="radio"]');
        if (radio) radio.checked = true;
      });
    });
  }
});

