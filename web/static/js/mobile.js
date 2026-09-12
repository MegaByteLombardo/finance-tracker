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
  const categoryGroup = document.getElementById('op-category-group');
  const destinationGroup = document.getElementById('op-destination-group');
  const labelOriginAccount = document.getElementById('label-origin-account');
  const catChips = document.querySelectorAll('.op-cat-chip');

  if (opTypeTabs.length > 0) {
    opTypeTabs.forEach(tab => {
      tab.addEventListener('click', () => {
        const type = tab.getAttribute('data-type');
        opTypeTabs.forEach(t => {
          t.classList.remove('is-active', 'type-expense', 'type-income', 'type-transfer');
          t.setAttribute('aria-selected', 'false');
        });
        tab.classList.add('is-active');
        tab.setAttribute('aria-selected', 'true');
        if (opInputType) opInputType.value = type;

        const mobileForm = document.getElementById('mobile-operation-form');
        const srcSelect = document.getElementById('op-account-select');

        if (type === 'EXPENSE') {
          tab.classList.add('type-expense');
          if (opSubmitBtn) {
            opSubmitBtn.textContent = 'Registrar Gasto';
            opSubmitBtn.className = 'op-submit-btn btn-expense';
          }
          if (categoryGroup) categoryGroup.style.display = 'flex';
          if (destinationGroup) destinationGroup.style.display = 'none';
          if (labelOriginAccount) labelOriginAccount.textContent = 'Cuenta';
          if (mobileForm) mobileForm.action = '/transactions';
          if (srcSelect) srcSelect.name = 'account_id';
        } else if (type === 'INCOME') {
          tab.classList.add('type-income');
          if (opSubmitBtn) {
            opSubmitBtn.textContent = 'Registrar Ingreso';
            opSubmitBtn.className = 'op-submit-btn btn-income';
          }
          if (categoryGroup) categoryGroup.style.display = 'flex';
          if (destinationGroup) destinationGroup.style.display = 'none';
          if (labelOriginAccount) labelOriginAccount.textContent = 'Cuenta de abono';
          if (mobileForm) mobileForm.action = '/transactions';
          if (srcSelect) srcSelect.name = 'account_id';
        } else if (type === 'TRANSFER') {
          tab.classList.add('type-transfer');
          if (opSubmitBtn) {
            opSubmitBtn.textContent = 'Registrar Transferencia';
            opSubmitBtn.className = 'op-submit-btn btn-transfer';
          }
          if (categoryGroup) categoryGroup.style.display = 'none';
          if (destinationGroup) destinationGroup.style.display = 'flex';
          if (labelOriginAccount) labelOriginAccount.textContent = 'Cuenta de origen';
          if (mobileForm) mobileForm.action = '/transfers';
          if (srcSelect) srcSelect.name = 'source_account_id';
        }
      });
    });

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

