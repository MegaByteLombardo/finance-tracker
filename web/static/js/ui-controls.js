// Finance Tracker - Custom UI Controls (Custom Dropdowns & Datepicker)
// Seamlessly wraps native <select> and <input type="date"> elements while
// keeping native form submission, HTMX requests, and change events fully working.
(function () {
  'use strict';

  var MONTH_NAMES = [
    'Enero', 'Febrero', 'Marzo', 'Abril', 'Mayo', 'Junio',
    'Julio', 'Agosto', 'Septiembre', 'Octubre', 'Noviembre', 'Diciembre'
  ];
  var DAY_NAMES = ['Lu', 'Ma', 'Mi', 'Ju', 'Vi', 'Sá', 'Do'];

  // Pad helper
  function pad2(n) {
    return n < 10 ? '0' + n : '' + n;
  }

  // Format date YYYY-MM-DD
  function formatDateISO(year, month, day) {
    return year + '-' + pad2(month + 1) + '-' + pad2(day);
  }

  // Parse YYYY-MM-DD to date components
  function parseDateISO(str) {
    if (!str || typeof str !== 'string') return null;
    var parts = str.split('-');
    if (parts.length !== 3) return null;
    var y = parseInt(parts[0], 10);
    var m = parseInt(parts[1], 10) - 1;
    var d = parseInt(parts[2], 10);
    if (isNaN(y) || isNaN(m) || isNaN(d)) return null;
    return { year: y, month: m, day: d };
  }

  // Friendly date label
  function formatFriendlyDate(str) {
    var p = parseDateISO(str);
    if (!p) return '';
    return p.day + ' ' + MONTH_NAMES[p.month].substring(0, 3) + ' ' + p.year;
  }

  // Close all open menus
  function closeAllDropdowns(exceptWrapper) {
    document.querySelectorAll('.ft-select-dropdown.is-open, .ft-datepicker-dropdown.is-open').forEach(function (dd) {
      if (!exceptWrapper || !exceptWrapper.contains(dd)) {
        dd.classList.remove('is-open');
        var trigger = dd.parentElement.querySelector('.ft-trigger-active');
        if (trigger) {
          trigger.classList.remove('ft-trigger-active');
          trigger.setAttribute('aria-expanded', 'false');
        }
      }
    });
  }

  // =========================================================================
  // 1. CUSTOM SELECT
  // =========================================================================
  function initCustomSelect(select) {
    if (select._customSelectInit || select.dataset.native === 'true') return;
    select._customSelectInit = true;

    var wrapper = document.createElement('div');
    wrapper.className = 'ft-select-wrapper';
    if (select.classList.contains('form-control-sm') || select.classList.contains('form-select-sm')) {
      wrapper.classList.add('ft-select-sm');
    }

    // Insert wrapper right before select and move select inside
    select.parentNode.insertBefore(wrapper, select);
    wrapper.appendChild(select);

    // Hide native select visually while keeping it fully interactive in DOM
    select.classList.add('ft-native-hidden');

    // Trigger button
    var trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = 'ft-select-trigger';
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');

    var textSpan = document.createElement('span');
    textSpan.className = 'ft-select-text';

    var arrowSpan = document.createElement('span');
    arrowSpan.className = 'ft-select-arrow';
    arrowSpan.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"></polyline></svg>';

    trigger.appendChild(textSpan);
    trigger.appendChild(arrowSpan);
    wrapper.appendChild(trigger);

    // Dropdown list
    var dropdown = document.createElement('div');
    dropdown.className = 'ft-select-dropdown';
    dropdown.setAttribute('role', 'listbox');
    wrapper.appendChild(dropdown);

    function updateOptions() {
      dropdown.innerHTML = '';
      var selectedText = '';
      var hasSelected = false;

      Array.from(select.options).forEach(function (opt) {
        var optEl = document.createElement('div');
        optEl.className = 'ft-select-option';
        optEl.setAttribute('role', 'option');
        optEl.dataset.value = opt.value;
        optEl.textContent = opt.textContent;

        if (opt.selected) {
          optEl.classList.add('is-selected');
          selectedText = opt.textContent;
          hasSelected = true;
        }

        if (opt.disabled) {
          optEl.classList.add('is-disabled');
        } else {
          optEl.addEventListener('click', function (e) {
            e.stopPropagation();
            if (select.value !== opt.value) {
              select.value = opt.value;
              select.dispatchEvent(new Event('change', { bubbles: true }));
              select.dispatchEvent(new Event('input', { bubbles: true }));
            }
            updateTriggerText();
            closeAllDropdowns();
          });
        }

        dropdown.appendChild(optEl);
      });

      if (!hasSelected && select.options.length > 0) {
        selectedText = select.options[0].textContent;
      }
      textSpan.textContent = selectedText || 'Seleccionar...';
    }

    function updateTriggerText() {
      var selectedOpt = select.options[select.selectedIndex];
      textSpan.textContent = selectedOpt ? selectedOpt.textContent : 'Seleccionar...';

      // Update is-selected in dropdown
      dropdown.querySelectorAll('.ft-select-option').forEach(function (optEl) {
        if (selectedOpt && optEl.dataset.value === selectedOpt.value) {
          optEl.classList.add('is-selected');
        } else {
          optEl.classList.remove('is-selected');
        }
      });
    }

    trigger.addEventListener('click', function (e) {
      e.stopPropagation();
      var isOpen = dropdown.classList.contains('is-open');
      closeAllDropdowns(isOpen ? null : wrapper);

      if (!isOpen) {
        dropdown.classList.add('is-open');
        trigger.classList.add('ft-trigger-active');
        trigger.setAttribute('aria-expanded', 'true');

        // Scroll selected into view
        var selectedItem = dropdown.querySelector('.is-selected');
        if (selectedItem) {
          selectedItem.scrollIntoView({ block: 'nearest' });
        }
      } else {
        dropdown.classList.remove('is-open');
        trigger.classList.remove('ft-trigger-active');
        trigger.setAttribute('aria-expanded', 'false');
      }
    });

    // Listen to native select change (e.g. from code reset)
    select.addEventListener('change', updateTriggerText);

    // Initial build
    updateOptions();

    // Rebuild options if options change dynamically (e.g. htmx swap)
    var observer = new MutationObserver(function () {
      updateOptions();
    });
    observer.observe(select, { childList: true, subtree: true });
  }

  // =========================================================================
  // 2. CUSTOM DATEPICKER
  // =========================================================================
  function initCustomDatePicker(input) {
    if (input._customDatePickerInit || input.dataset.native === 'true') return;
    input._customDatePickerInit = true;

    var wrapper = document.createElement('div');
    wrapper.className = 'ft-datepicker-wrapper';
    if (input.classList.contains('form-control-sm') || input.classList.contains('form-input-sm')) {
      wrapper.classList.add('ft-datepicker-sm');
    }

    // Insert wrapper right before input and move input inside
    input.parentNode.insertBefore(wrapper, input);
    wrapper.appendChild(input);

    // Hide native datepicker visually while keeping form submission intact
    input.classList.add('ft-native-hidden');

    // Trigger button
    var trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = 'ft-datepicker-trigger';
    trigger.setAttribute('aria-haspopup', 'dialog');
    trigger.setAttribute('aria-expanded', 'false');

    var textSpan = document.createElement('span');
    textSpan.className = 'ft-datepicker-text';

    var iconSpan = document.createElement('span');
    iconSpan.className = 'ft-datepicker-icon';
    iconSpan.innerHTML = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="18" rx="2" ry="2"></rect><line x1="16" y1="2" x2="16" y2="6"></line><line x1="8" y1="2" x2="8" y2="6"></line><line x1="3" y1="10" x2="21" y2="10"></line></svg>';

    trigger.appendChild(textSpan);
    trigger.appendChild(iconSpan);
    wrapper.appendChild(trigger);

    // Dropdown container
    var dropdown = document.createElement('div');
    dropdown.className = 'ft-datepicker-dropdown';
    wrapper.appendChild(dropdown);

    // Calendar state (view year & month)
    var today = new Date();
    var viewYear = today.getFullYear();
    var viewMonth = today.getMonth();

    if (input.value) {
      var p = parseDateISO(input.value);
      if (p) {
        viewYear = p.year;
        viewMonth = p.month;
      }
    }

    function updateTriggerText() {
      if (input.value) {
        textSpan.textContent = formatFriendlyDate(input.value);
        trigger.classList.add('has-value');
      } else {
        textSpan.textContent = input.getAttribute('placeholder') || 'Seleccionar fecha...';
        trigger.classList.remove('has-value');
      }
    }

    function renderCalendar() {
      dropdown.innerHTML = '';

      // Header: Month Nav
      var header = document.createElement('div');
      header.className = 'dp-header';

      var prevBtn = document.createElement('button');
      prevBtn.type = 'button';
      prevBtn.className = 'dp-nav-btn';
      prevBtn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 18 9 12 15 6"></polyline></svg>';
      prevBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        viewMonth--;
        if (viewMonth < 0) {
          viewMonth = 11;
          viewYear--;
        }
        renderCalendar();
      });

      var title = document.createElement('div');
      title.className = 'dp-title';
      title.textContent = MONTH_NAMES[viewMonth] + ' ' + viewYear;

      var nextBtn = document.createElement('button');
      nextBtn.type = 'button';
      nextBtn.className = 'dp-nav-btn';
      nextBtn.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6"></polyline></svg>';
      nextBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        viewMonth++;
        if (viewMonth > 11) {
          viewMonth = 0;
          viewYear++;
        }
        renderCalendar();
      });

      header.appendChild(prevBtn);
      header.appendChild(title);
      header.appendChild(nextBtn);
      dropdown.appendChild(header);

      // Weekdays bar
      var weekdaysRow = document.createElement('div');
      weekdaysRow.className = 'dp-weekdays';
      DAY_NAMES.forEach(function (d) {
        var w = document.createElement('span');
        w.textContent = d;
        weekdaysRow.appendChild(w);
      });
      dropdown.appendChild(weekdaysRow);

      // Days grid
      var daysGrid = document.createElement('div');
      daysGrid.className = 'dp-days-grid';

      // First day of month and total days
      var firstDay = new Date(viewYear, viewMonth, 1).getDay(); // 0 is Sunday
      // Convert to Monday = 0: (day + 6) % 7
      var startOffset = (firstDay + 6) % 7;
      var daysInMonth = new Date(viewYear, viewMonth + 1, 0).getDate();
      var daysInPrevMonth = new Date(viewYear, viewMonth, 0).getDate();

      var selectedISO = input.value || '';
      var todayISO = formatDateISO(today.getFullYear(), today.getMonth(), today.getDate());

      // Previous month padding days
      for (var i = startOffset - 1; i >= 0; i--) {
        var pDay = daysInPrevMonth - i;
        var pCell = document.createElement('button');
        pCell.type = 'button';
        pCell.className = 'dp-day dp-day-muted';
        pCell.textContent = pDay;
        (function (dayNum) {
          pCell.addEventListener('click', function (e) {
            e.stopPropagation();
            viewMonth--;
            if (viewMonth < 0) {
              viewMonth = 11;
              viewYear--;
            }
            selectDate(formatDateISO(viewYear, viewMonth, dayNum));
          });
        })(pDay);
        daysGrid.appendChild(pCell);
      }

      // Current month days
      for (var d = 1; d <= daysInMonth; d++) {
        var dayCell = document.createElement('button');
        dayCell.type = 'button';
        dayCell.className = 'dp-day';
        dayCell.textContent = d;

        var thisISO = formatDateISO(viewYear, viewMonth, d);
        if (thisISO === selectedISO) {
          dayCell.classList.add('is-selected');
        }
        if (thisISO === todayISO) {
          dayCell.classList.add('is-today');
        }

        (function (iso) {
          dayCell.addEventListener('click', function (e) {
            e.stopPropagation();
            selectDate(iso);
          });
        })(thisISO);

        daysGrid.appendChild(dayCell);
      }

      // Next month padding days to complete grid (42 cells max)
      var totalCells = startOffset + daysInMonth;
      var nextMonthDays = totalCells % 7 === 0 ? 0 : 7 - (totalCells % 7);
      for (var n = 1; n <= nextMonthDays; n++) {
        var nCell = document.createElement('button');
        nCell.type = 'button';
        nCell.className = 'dp-day dp-day-muted';
        nCell.textContent = n;
        (function (dayNum) {
          nCell.addEventListener('click', function (e) {
            e.stopPropagation();
            viewMonth++;
            if (viewMonth > 11) {
              viewMonth = 0;
              viewYear++;
            }
            selectDate(formatDateISO(viewYear, viewMonth, dayNum));
          });
        })(n);
        daysGrid.appendChild(nCell);
      }

      dropdown.appendChild(daysGrid);

      // Footer with Quick Actions: "Hoy" & "Limpiar"
      var footer = document.createElement('div');
      footer.className = 'dp-footer';

      var todayBtn = document.createElement('button');
      todayBtn.type = 'button';
      todayBtn.className = 'dp-action-btn';
      todayBtn.textContent = 'Hoy';
      todayBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        viewYear = today.getFullYear();
        viewMonth = today.getMonth();
        selectDate(todayISO);
      });

      var clearBtn = document.createElement('button');
      clearBtn.type = 'button';
      clearBtn.className = 'dp-action-btn dp-action-clear';
      clearBtn.textContent = 'Limpiar';
      clearBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        input.value = '';
        input.dispatchEvent(new Event('change', { bubbles: true }));
        input.dispatchEvent(new Event('input', { bubbles: true }));
        updateTriggerText();
        closeAllDropdowns();
      });

      footer.appendChild(todayBtn);
      footer.appendChild(clearBtn);
      dropdown.appendChild(footer);
    }

    function selectDate(isoString) {
      if (input.value !== isoString) {
        input.value = isoString;
        input.dispatchEvent(new Event('change', { bubbles: true }));
        input.dispatchEvent(new Event('input', { bubbles: true }));
      }
      updateTriggerText();
      closeAllDropdowns();
    }

    trigger.addEventListener('click', function (e) {
      e.stopPropagation();
      var isOpen = dropdown.classList.contains('is-open');
      closeAllDropdowns(isOpen ? null : wrapper);

      if (!isOpen) {
        // Sync view with selected or today
        if (input.value) {
          var p = parseDateISO(input.value);
          if (p) {
            viewYear = p.year;
            viewMonth = p.month;
          }
        } else {
          viewYear = today.getFullYear();
          viewMonth = today.getMonth();
        }
        renderCalendar();
        dropdown.classList.add('is-open');
        trigger.classList.add('ft-trigger-active');
        trigger.setAttribute('aria-expanded', 'true');
      } else {
        dropdown.classList.remove('is-open');
        trigger.classList.remove('ft-trigger-active');
        trigger.setAttribute('aria-expanded', 'false');
      }
    });

    // Listen to native input change (e.g. from code reset)
    input.addEventListener('change', updateTriggerText);

    // Initial setup
    updateTriggerText();
  }

  // =========================================================================
  // SCAN & INITIALISE
  // =========================================================================
  function initAll(root) {
    var container = root || document;

    // 1. Initialise selects
    container.querySelectorAll('select').forEach(function (sel) {
      initCustomSelect(sel);
    });

    // 2. Initialise date inputs
    container.querySelectorAll('input[type="date"]').forEach(function (inp) {
      initCustomDatePicker(inp);
    });
  }

  // Global click & key listeners
  document.addEventListener('click', function (e) {
    if (!e.target.closest('.ft-select-wrapper') && !e.target.closest('.ft-datepicker-wrapper')) {
      closeAllDropdowns();
    }
  });

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') {
      closeAllDropdowns();
    }
  });

  // Init on DOM ready and HTMX load
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () {
      initAll(document);
    });
  } else {
    initAll(document);
  }

  document.addEventListener('htmx:load', function (e) {
    initAll(e.detail.elt || document);
  });
})();
