/* WezRelay browser client. SPDX-License-Identifier: AGPL-3.0-only */
'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const encoder = new TextEncoder();
  const instances = new Map();
  const drafts = new Map();
  let panes = [], activeID = null, fit = false, ctrl = false, refreshing = false, generation = 0;
  const views = [];
  let activeView = null, nextView = 1, initialized = false;
  let token = '';
  try { token = sessionStorage.getItem('wezrelay_token') || ''; } catch (_) { /* Storage may be disabled. */ }
  const url = new URL(location.href);
  if (url.searchParams.has('token')) {
    token = url.searchParams.get('token');
    url.searchParams.delete('token');
    history.replaceState(null, '', url.pathname + url.search + url.hash);
    try { sessionStorage.setItem('wezrelay_token', token); } catch (_) {}
  }
  $('token').value = token;
  function notice(message = '') { $('notice').textContent = message; $('notice').hidden = !message; }
  async function api(path, method = 'GET') {
    const response = await fetch(path, { method, headers: token ? { Authorization: 'Bearer ' + token } : {}, cache: 'no-store' });
    const authRequired = response.headers.get('X-WezRelay-Auth') === 'required';
    $('auth-toggle').hidden = !authRequired;
    if (!authRequired) $('auth-form').hidden = true;
    if (!response.ok) {
      if (response.status === 401) $('auth-form').hidden = false;
      const detail = (await response.text()).trim().slice(0, 350);
      throw new Error(detail || `Server returned HTTP ${response.status}`);
    }
    return response;
  }
  let fileDirectory = '', fileObjectURL = null, fileGeneration = 0, fileEntries = [], filePath = '';
  try { $('files-sort').value = localStorage.getItem('wezrelay_file_sort') || 'name'; } catch (_) {}
  if (!$('files-sort').value) $('files-sort').value = 'name';
  function renderFileEntries() {
    const order = $('files-sort').value;
    const nameOrder = (a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()) || a.name.localeCompare(b.name);
    const entries = [...fileEntries].sort((a, b) => {
      if (a.directory !== b.directory) return a.directory ? -1 : 1;
      if (order.startsWith('modified')) {
        const difference = (Date.parse(a.modified) || 0) - (Date.parse(b.modified) || 0);
        return (order === 'modified' ? -difference : difference) || nameOrder(a, b);
      }
      return (order === 'name-desc' ? -1 : 1) * nameOrder(a, b);
    });
    $('files-list').replaceChildren();
    for (const entry of entries) {
      const path = (fileDirectory ? fileDirectory + '/' : '') + entry.name;
      const row = document.createElement('div'); row.className = 'file-row';
      const button = document.createElement('button'); button.className = 'file-open';
      button.textContent = (entry.directory ? '📁 ' : '📄 ') + entry.name;
      button.onclick = () => openFilePath(path, entry.directory); row.append(button);
      if (!entry.directory) {
        const download = document.createElement('button'); download.textContent = 'Download';
        download.setAttribute('aria-label', 'Download ' + entry.name);
        download.onclick = () => downloadFile(path, download); row.append(download);
      }
      $('files-list').append(row);
    }
  }
  async function downloadFile(path, button) {
    button.disabled = true;
    try {
      // With no token, let the browser stream directly to its download manager.
      // Bearer authentication uses fetch so credentials never appear in a URL.
      const link = document.createElement('a'); link.download = path.split('/').pop();
      let objectURL;
      if (token) {
        const response = await api(fileAPI('download', path));
        objectURL = URL.createObjectURL(await response.blob()); link.href = objectURL;
      } else link.href = fileAPI('download', path);
      document.body.append(link); link.click(); link.remove();
      if (objectURL) setTimeout(() => URL.revokeObjectURL(objectURL), 60000);
    } catch (error) { $('files-status').textContent = error.message; }
    finally { button.disabled = false; }
  }
  $('files-sort').onchange = () => {
    try { localStorage.setItem('wezrelay_file_sort', $('files-sort').value); } catch (_) {}
    renderFileEntries();
  };
  $('files-download').onclick = () => downloadFile(filePath, $('files-download'));
  function clearFilePreview() {
    if (fileObjectURL) URL.revokeObjectURL(fileObjectURL);
    fileObjectURL = null; $('files-content').replaceChildren();
  }
  function fileAPI(action, path) {
    return '/api/files/' + action + '?root=' + encodeURIComponent($('files-root').value) + '&path=' + encodeURIComponent(path || '.');
  }
  async function openFilePath(path, directory = false) {
    const request = ++fileGeneration;
    $('files-status').textContent = 'Loading…';
    if (!directory) {
      clearFilePreview(); filePath = path;
      $('files-path').value = path; $('files-name').textContent = path;
      $('files-preview').hidden = false; $('files-list').hidden = true;
    }
    try {
      // Open path accepts either files or directories; list first, then preview.
      let response;
      if (directory) response = await api(fileAPI('list', path));
      else response = await api(fileAPI('read', path));
      if (request !== fileGeneration) return;
      clearFilePreview();
      if (directory) {
        const entries = await response.json();
        if (request !== fileGeneration) return;
        fileDirectory = path === '.' ? '' : path; $('files-path').value = fileDirectory;
        $('files-preview').hidden = true; $('files-list').hidden = false;
        fileEntries = entries; renderFileEntries();
        $('files-status').textContent = entries.length + ' entries · Read only';
      } else {
        $('files-path').value = path; $('files-name').textContent = path;
        const contentType = response.headers.get('Content-Type') || '';
        if (contentType.startsWith('image/')) {
          const blob = await response.blob(); if (request !== fileGeneration) return;
          fileObjectURL = URL.createObjectURL(blob);
          const image = document.createElement('img'); image.alt = path; image.src = fileObjectURL;
          image.onclick = () => image.classList.toggle('full-size');
          $('files-content').append(image);
          $('files-status').textContent = 'Tap image for original size; scroll to explore.';
        } else {
          const text = await response.text(); if (request !== fileGeneration) return;
          const pre = document.createElement('pre'); pre.textContent = text; $('files-content').append(pre);
          $('files-status').textContent = 'Read-only UTF-8 text · ' + text.split('\n').length + ' lines';
        }
        $('files-preview').hidden = false; $('files-list').hidden = true;
      }
    } catch (error) { if (request === fileGeneration) $('files-status').textContent = error.message; }
  }
  $('files-open').onclick = async () => {
    $('files-dialog').showModal();
    try {
      const response = await api('/api/files/roots'); const roots = await response.json();
      $('files-root').replaceChildren(...roots.map((root, id) => new Option(root.name + ' · ' + root.path, String(id))));
      await openFilePath('', true);
    } catch (error) { $('files-status').textContent = error.message; }
  };
  $('files-close').onclick = () => $('files-dialog').close();
  $('files-dialog').addEventListener('close', () => { ++fileGeneration; clearFilePreview(); });
  $('files-root').onchange = () => openFilePath('', true);
  $('files-back').onclick = () => openFilePath(fileDirectory, true);
  $('files-up').onclick = () => openFilePath(fileDirectory.split('/').slice(0, -1).join('/'), true);
  $('files-form').onsubmit = async event => {
    event.preventDefault();
    const path = $('files-path').value.replace(/^\/+/, '').replace(/\/$/, '');
    // Directory requests return 400 for regular files; use the read endpoint then.
    try {
      const response = await api(fileAPI('list', path));
      if (response.ok) { await openFilePath(path, true); return; }
    } catch (_) { /* Try a regular file path next. */ }
    await openFilePath(path);
  };
  function label(p) { return `T${p.tab_id ?? '?'} · #${p.wez_pane_id >= 0 ? p.wez_pane_id : p.id}  ${p.title || 'Terminal'}`; }
  function send(inst, data) {
    if (!inst || inst.ws?.readyState !== WebSocket.OPEN) { notice('Terminal is reconnecting. Wait for Connected before typing.'); return; }
    inst.ws.send(encoder.encode(data));
  }
  function desktopSize(p) {
    return { cols: p.desktop_cols || p.cols || 80, rows: p.desktop_rows || p.rows || 24 };
  }
  function resize(inst, restore = false) {
    if (inst.card.hidden && !restore) return;
    inst.preserveViewport?.();
    inst.mount.classList.toggle('match', !fit || inst.meta.mode !== 'shim');
    let size = inst.desktop;
    if (fit && inst.meta.mode === 'shim') {
      inst.mount.firstElementChild.style.height = '';
      const proposed = inst.addon.proposeDimensions();
      if (!proposed || proposed.cols < 2 || proposed.rows < 1) return;
      size = proposed;
    }
    const cols = Math.min(65535, Math.max(2, size.cols)), rows = Math.min(65535, Math.max(1, size.rows));
    if (inst.term.cols !== cols || inst.term.rows !== rows) inst.term.resize(cols, rows);
    inst.updatePan?.();
    inst.syncViewport?.();
    if (inst.meta.mode === 'shim' && (fit || restore) && inst.ws?.readyState === WebSocket.OPEN) {
      const key = `${cols}:${rows}`;
      if (restore || inst.sentSize !== key) { inst.ws.send(JSON.stringify({ type: 'resize', cols, rows })); inst.sentSize = key; inst.browserSized = true; }
    }
  }
  function connect(inst) {
    if (!instances.has(inst.id)) return;
    clearTimeout(inst.timer);
    const wsURL = new URL('/ws', location.href);
    wsURL.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    wsURL.searchParams.set('id', inst.id);
    if (token) wsURL.searchParams.set('token', token);
    const ws = new WebSocket(wsURL);
    inst.ws = ws; ws.binaryType = 'arraybuffer'; inst.state.textContent = 'Connecting…';
    ws.onopen = () => {
      if (inst.ws !== ws) return;
      inst.term.reset(); inst.retries = 0; inst.sentSize = null;
      inst.state.textContent = 'Connected'; resize(inst);
    };
    ws.onmessage = event => {
      if (inst.ws !== ws) return;
      if (event.data instanceof ArrayBuffer) {
        inst.preserveViewport();
        inst.term.write(new Uint8Array(event.data), () => inst.syncViewport());
      }
    };
    ws.onerror = () => { inst.state.textContent = 'Connection interrupted'; };
    ws.onclose = () => {
      if (!instances.has(inst.id) || inst.ws !== ws) return;
      inst.state.textContent = 'Reconnecting…';
      inst.timer = setTimeout(() => connect(inst), Math.min(15000, 1000 * 2 ** Math.min(inst.retries++, 4)));
    };
  }
  function dispose(inst) {
    if (inst.browserSized) { const saved = fit; fit = false; resize(inst, true); fit = saved; }
    inst.panObserver?.disconnect();
    inst.viewportObserver?.disconnect();
    cancelAnimationFrame(inst.viewportFrame);
    clearTimeout(inst.timer); inst.ws.onclose = null; inst.ws.close(); inst.term.dispose(); inst.card.remove(); instances.delete(inst.id);
  }
  function create(p) {
    const card = document.createElement('section'); card.className = 'term-card'; card.dataset.paneId = p.id;
    const header = document.createElement('div'); header.className = 'term-header';
    const title = document.createElement('span'); title.className = 'title';
    const state = document.createElement('span'); state.className = 'term-state';
    header.append(title, state);
    const mount = document.createElement('div'); mount.className = 'term-mount match';
    const inner = document.createElement('div'); inner.className = 'term-inner'; mount.append(inner); card.append(header, mount); $('workspace').append(card);
    const term = new Terminal({ cols: desktopSize(p).cols, rows: desktopSize(p).rows, cursorBlink: true, fontSize: innerWidth < 760 ? 12 : 13, fontFamily: 'ui-monospace, "DejaVu Sans Mono", monospace', scrollback: 5000, theme: { background: '#10171f', foreground: '#e3eaf1', cursor: '#7ce4c1', selectionBackground: '#31594e' } });
    const addon = new FitAddon.FitAddon(); term.loadAddon(addon); term.open(inner);
    const inst = { id: p.id, meta: p, desktop: desktopSize(p), card, mount, title, state, term, addon, ws: null, retries: 0 };
    instances.set(p.id, inst);
    card.addEventListener('pointerdown', () => { if (activeID !== p.id) { activeID = p.id; render(); } });
    term.onData(data => {
      if (ctrl && !inst.pastingDraft && data.length === 1) {
        const code = data.toUpperCase().charCodeAt(0);
        if (code >= 64 && code <= 95) data = String.fromCharCode(code - 64);
        ctrl = false; $('ctrl-btn').setAttribute('aria-pressed', 'false');
      }
      send(inst, data);
    });
    term.onBinary(data => {
      if (inst.ws?.readyState === WebSocket.OPEN) inst.ws.send(Uint8Array.from(data, c => c.charCodeAt(0)));
    });
    term.attachCustomKeyEventHandler(event => {
      if (event.key === 'Enter' && event.shiftKey) { if (event.type === 'keydown') send(inst, '\x1b\r'); return false; }
      return true;
    });
    installComposer(inst);
    installPan(inst);
    installViewport(inst);
    connect(inst); return inst;
  }
  // A terminal stream cannot undo an autocorrection reliably: its cursor may
  // belong to any application. Let the browser edit a draft, then paste only the
  // final text through xterm so bracketed-paste mode is respected.
  function installComposer(inst) {
    const box = document.createElement('details'); box.className = 'text-composer';
    box.open = matchMedia('(max-width: 760px), (pointer: coarse)').matches;
    const summary = document.createElement('summary'); summary.textContent = 'Compose text';
    const row = document.createElement('div'); row.className = 'composer-row';
    const draft = document.createElement('textarea');
    draft.rows = 2; draft.placeholder = 'Type or swipe here, then Send text';
    draft.setAttribute('aria-label', 'Terminal text draft');
    draft.setAttribute('autocorrect', 'on'); draft.setAttribute('autocapitalize', 'sentences'); draft.spellcheck = true;
    draft.value = drafts.get(inst.id) || '';
    const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Send text';
    const hint = document.createElement('small'); hint.textContent = 'Edit with suggestions before sending. Send text adds no Enter; use enter ↵ afterward to submit.';
    let composing = false;
    const update = () => { drafts.set(inst.id, draft.value); button.disabled = composing || !draft.value; };
    draft.addEventListener('input', update);
    draft.addEventListener('compositionstart', () => { composing = true; update(); });
    draft.addEventListener('compositionend', () => { composing = false; update(); });
    button.onclick = () => {
      if (composing || !draft.value) return;
      if (inst.ws?.readyState !== WebSocket.OPEN) { notice('Terminal is reconnecting. Your draft has been kept.'); return; }
      inst.pastingDraft = true;
      try { inst.term.paste(draft.value); } finally { inst.pastingDraft = false; }
      draft.value = ''; update(); draft.focus();
    };
    row.append(draft, button); box.append(summary, row, hint); inst.card.append(box);
    update();
  }
  // Match Desktop keeps every desktop row in xterm. Give its own viewport the
  // full screen height, then pan the clipped screen in the outer mount. Otherwise
  // xterm and the browser disagree about the last visible row and scroll range.
  function installViewport(inst) {
    const { mount, term } = inst;
    const inner = mount.firstElementChild;
    const screen = inner.querySelector('.xterm-screen');
    inst.followTail = true;
    inst.preserveViewport = () => { inst.ignoreViewportScroll = performance.now() + 250; };
    inst.preserveViewport();
    let syncing = false;
    inst.syncViewport = () => {
      cancelAnimationFrame(inst.viewportFrame);
      inst.viewportFrame = requestAnimationFrame(() => {
        if (!instances.has(inst.id)) return;
        const match = !fit || inst.meta.mode !== 'shim';
        inner.style.height = match ? `${screen.offsetHeight}px` : '';
        // Focus can scroll xterm's helper textarea and its ancestors on Android.
        // Only the mount owns the clipped desktop screen's vertical position.
        inner.scrollTop = 0;
        inner.querySelector('.xterm').scrollTop = 0;
        if (inst.followTail) {
          syncing = true;
          inst.preserveViewport();
          term.scrollToBottom();
          mount.scrollTop = mount.scrollHeight;
          syncing = false;
        }
        inst.updatePan?.();
      });
    };
    mount.addEventListener('scroll', () => {
      if (!syncing && performance.now() > inst.ignoreViewportScroll) inst.followTail = mount.scrollHeight - mount.clientHeight - mount.scrollTop < 3 && term.buffer.active.viewportY === term.buffer.active.baseY;
    }, { passive: true });
    term.onScroll(() => {
      if (performance.now() > inst.ignoreViewportScroll) inst.followTail = term.buffer.active.viewportY === term.buffer.active.baseY && mount.scrollHeight - mount.clientHeight - mount.scrollTop < 3;
    });
    // Consume vertical motion in the clipped desktop screen before allowing
    // xterm to traverse its older scrollback. Horizontal gestures remain below.
    let remainder = 0;
    const scrollScreen = delta => {
      if (fit && inst.meta.mode === 'shim') return false;
      inst.ignoreViewportScroll = 0;
      const buffer = term.buffer.active;
      const lineHeight = screen.offsetHeight / term.rows || 14;
      // Returning from history must reach the live screen before panning down it.
      if (delta > 0 && buffer.viewportY < buffer.baseY) {
        const historyDelta = Math.min(delta, (buffer.baseY - buffer.viewportY) * lineHeight);
        remainder += historyDelta;
        const lines = Math.trunc(remainder / lineHeight);
        term.scrollLines(lines); remainder -= lines * lineHeight;
        if (buffer.viewportY === buffer.baseY) mount.scrollTop += delta - historyDelta;
      } else {
        const before = mount.scrollTop;
        mount.scrollTop += delta;
        const unused = delta - (mount.scrollTop - before);
        if (unused < 0) {
          remainder += unused;
          const lines = Math.trunc(remainder / lineHeight);
          term.scrollLines(lines); remainder -= lines * lineHeight;
        } else remainder = 0;
      }
      inst.followTail = mount.scrollHeight - mount.clientHeight - mount.scrollTop < 3 && buffer.viewportY === buffer.baseY;
      return true;
    };
    mount.addEventListener('wheel', event => {
      if (Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return;
      const delta = event.deltaY * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? mount.clientHeight : 1);
      if (scrollScreen(delta)) { event.preventDefault(); event.stopPropagation(); }
    }, { passive: false, capture: true });
    let touch = null;
    mount.addEventListener('touchstart', event => {
      touch = event.touches.length === 1 ? { x: event.touches[0].clientX, y: event.touches[0].clientY, axis: null } : null;
    }, { passive: true, capture: true });
    mount.addEventListener('touchmove', event => {
      if (!touch || event.touches.length !== 1) { touch = null; return; }
      const point = event.touches[0], dx = point.clientX - touch.x, dy = point.clientY - touch.y;
      if (!touch.axis && Math.max(Math.abs(dx), Math.abs(dy)) > 8) touch.axis = Math.abs(dy) > Math.abs(dx) ? 'y' : 'x';
      if (touch.axis !== 'y') return;
      touch.y = point.clientY;
      if (scrollScreen(-dy)) { event.preventDefault(); event.stopPropagation(); }
    }, { passive: false, capture: true });
    mount.addEventListener('touchend', () => { touch = null; }, { passive: true });
    mount.addEventListener('touchcancel', () => { touch = null; }, { passive: true });
    const focusChanged = () => { inst.preserveViewport(); inst.syncViewport(); };
    mount.addEventListener('focusin', focusChanged);
    mount.addEventListener('focusout', focusChanged);
    const observer = new ResizeObserver(inst.syncViewport);
    observer.observe(screen); observer.observe(mount);
    inst.viewportObserver = observer;
    inst.syncViewport();
  }
  // Capture horizontal swipes before xterm can cancel the browser gesture.
  // Vertical movement is handled above; taps retain terminal focus and selection.
  function installPan(inst) {
    const { mount, card } = inst;
    const bar = document.createElement('div'); bar.className = 'terminal-pan'; bar.hidden = true;
    const left = document.createElement('button'); left.textContent = '←'; left.setAttribute('aria-label', 'Pan terminal left');
    const right = document.createElement('button'); right.textContent = '→'; right.setAttribute('aria-label', 'Pan terminal right');
    const range = document.createElement('input'); range.type = 'range'; range.min = '0'; range.step = '1'; range.value = '0';
    range.setAttribute('aria-label', 'Terminal horizontal position');
    const hint = document.createElement('span'); hint.textContent = 'Swipe sideways or drag';
    bar.append(left, range, right, hint); card.append(bar);
    inst.updatePan = () => {
      const max = Math.max(0, mount.scrollWidth - mount.clientWidth);
      bar.hidden = max < 2; range.max = String(max); range.value = String(mount.scrollLeft);
      range.setAttribute('aria-valuetext', `${Math.round(max ? mount.scrollLeft / max * 100 : 0)}% from left`);
      left.disabled = mount.scrollLeft < 1; right.disabled = mount.scrollLeft >= max - 1;
    };
    range.oninput = () => { mount.scrollLeft = Number(range.value); inst.updatePan(); };
    left.onclick = () => { mount.scrollLeft -= mount.clientWidth * .75; inst.updatePan(); };
    right.onclick = () => { mount.scrollLeft += mount.clientWidth * .75; inst.updatePan(); };
    mount.addEventListener('scroll', inst.updatePan, { passive: true });
    let gesture = null;
    mount.addEventListener('touchstart', event => {
      gesture = event.touches.length === 1 ? { x: event.touches[0].clientX, y: event.touches[0].clientY, left: mount.scrollLeft, axis: null } : null;
    }, { passive: true, capture: true });
    mount.addEventListener('touchmove', event => {
      if (!gesture || event.touches.length !== 1) { gesture = null; return; }
      const dx = event.touches[0].clientX - gesture.x, dy = event.touches[0].clientY - gesture.y;
      if (!gesture.axis && Math.max(Math.abs(dx), Math.abs(dy)) > 8) gesture.axis = Math.abs(dx) > Math.abs(dy) ? 'x' : 'y';
      if (gesture.axis !== 'x' || mount.scrollWidth <= mount.clientWidth) return;
      event.preventDefault(); event.stopPropagation();
      mount.scrollLeft = gesture.left - dx; inst.updatePan();
    }, { passive: false, capture: true });
    const end = () => { gesture = null; };
    mount.addEventListener('touchend', end, { passive: true });
    mount.addEventListener('touchcancel', end, { passive: true });
    // Terminal font metrics settle after open; observe actual content geometry too.
    const observer = new ResizeObserver(inst.updatePan);
    observer.observe(mount); observer.observe(mount.firstElementChild);
    inst.panObserver = observer;
    inst.updatePan();
  }
  function active() { return views.find(view => view.id === activeView); }
  function addView() {
    initialized = true;
    const pane = panes.find(p => !views.some(v => v.pane === p.id)) || panes.find(p => p.id === activeID) || panes[0];
    const view = { id: nextView++, pane: pane?.id || null };
    views.push(view); activeView = view.id; render();
  }
  $('add-view').onclick = addView;
  let pickerActive = false;
  const picker = $('pane-select');
  picker.addEventListener('focus', () => { pickerActive = true; });
  picker.addEventListener('pointerdown', () => { pickerActive = true; });
  picker.addEventListener('keydown', () => { pickerActive = true; });
  picker.addEventListener('blur', () => { pickerActive = false; render(); });
  picker.addEventListener('keydown', event => { if (event.key === 'Escape') { pickerActive = false; setTimeout(render, 0); } });
  $('pane-select').onchange = () => {
    pickerActive = false;
    const view = active(); if (view) view.pane = $('pane-select').value || null;
    render();
  };
  function render() {
    if (!initialized && panes.length) {
      initialized = true;
      const requested = new URL(location.href).searchParams.get('pane');
      const pane = panes.find(p => p.id === requested) || panes.find(p => p.is_active) || panes[0];
      views.push({ id: nextView++, pane: pane.id }); activeView = views[0].id;
    }
    const view = active();
    const current = panes.find(p => p.id === view?.pane);
    activeID = current?.id || null;
    // Discovery never opens terminals. Only the active application view owns a subscription.
    for (const inst of instances.values()) if (inst.id !== activeID) dispose(inst);
    const select = $('pane-select');
    const options = panes.map(p => [p.id, `${p.workspace || 'default'} · ${label(p)} · ${p.mode === 'shim' ? 'LIVE' : 'SNAPSHOT'}`]);
    if (!current) options.unshift(['', view?.pane ? 'Pane unavailable — choose another pane' : 'Choose a pane']);
    const key = JSON.stringify(options);
    // Native phone pickers keep their own scroll/highlight state. Never mutate
    // their options or value while the user is choosing, including title polls.
    if (!pickerActive) {
      if (select.dataset.options !== key) {
        const existing = new Map([...select.options].map(option => [option.value, option]));
        const wanted = new Set(options.map(([value]) => value));
        for (const option of [...select.options]) if (!wanted.has(option.value)) option.remove();
        options.forEach(([value, text], index) => {
          const option = existing.get(value) || new Option(text, value);
          if (option.textContent !== text) option.textContent = text;
          if (select.options[index] !== option) select.insertBefore(option, select.options[index] || null);
        });
        select.dataset.options = key;
      }
      if (select.value !== (activeID || '')) select.value = activeID || '';
      select.disabled = !view || !panes.length;
    }
    $('workspace').className = 'single-view';
    $('workspace').hidden = !current; $('empty').hidden = !!current;
    $('pane-count').textContent = `${panes.length} available · ${views.length} open`;
    $('fit-toggle').textContent = fit ? 'Restore desktop size' : 'Resize to browser'; $('fit-toggle').setAttribute('aria-pressed', String(fit));
    $('fit-toggle').disabled = !current || current.mode !== 'shim';
    $('fit-toggle').title = current?.mode === 'shim' ? 'Explicitly resize the desktop shell to this browser; restore its desktop dimensions afterward' : 'Snapshot panes preserve their desktop size; swipe sideways or use the pan slider';
    $('mode-note').textContent = fit ? 'Browser sizing enabled for live panes · CLI snapshots keep desktop size' : 'Desktop size preserved · swipe sideways or use the pan slider';
    $('split-right-btn').disabled = $('split-down-btn').disabled = !current || current.wez_pane_id < 0;
    $('empty').querySelector('p').textContent = !view ? 'Open a view tab, then choose an existing desktop pane from the dropdown.' : !panes.length ? 'No terminal panes are connected. Open WezTerm on your desktop, or create a terminal tab.' : 'Choose an available pane from the dropdown for this view tab.';
    const list = $('pane-list'); list.replaceChildren();
    for (const item of views) {
      const p = panes.find(p => p.id === item.pane);
      const wrapper = document.createElement('div'); wrapper.className = 'view-tab' + (item.id === activeView ? ' active' : '');
      const button = document.createElement('button'); button.className = 'pane-btn';
      button.setAttribute('role', 'tab'); button.setAttribute('aria-selected', String(item.id === activeView));
      button.textContent = p ? label(p) : 'Choose pane'; button.title = button.textContent;
      button.onclick = () => { activeView = item.id; render(); };
      const close = document.createElement('button'); close.className = 'close-view'; close.textContent = '×'; close.setAttribute('aria-label', `Close view ${p ? label(p) : item.id}`);
      close.onclick = () => {
        const index = views.indexOf(item); views.splice(index, 1);
        if (activeView === item.id) activeView = (views[index] || views[index - 1])?.id || null;
        render();
      };
      wrapper.append(button, close); list.append(wrapper);
    }
    if (current) {
      let inst = instances.get(current.id);
      if (!inst) inst = create(current);
      if ((!fit && !inst.browserSized) || (current.desktop_cols && current.desktop_rows)) inst.desktop = desktopSize(current);
      if (!fit && inst.browserSized && current.cols === inst.desktop.cols && current.rows === inst.desktop.rows) inst.browserSized = false;
      inst.meta = current; inst.card.hidden = false; inst.card.classList.add('focused');
      inst.title.textContent = `${label(current)} · ${current.mode === 'shim' ? 'Live PTY' : 'CLI snapshot'}`;
      requestAnimationFrame(() => resize(inst));
    }
  }
  async function refresh() {
    if (refreshing) return;
    refreshing = true; const started = generation;
    try {
      const response = await api('/api/panes'); const result = await response.json();
      if (started !== generation) return;
      if (result !== null && !Array.isArray(result)) throw new Error('Server returned an invalid pane list.');
      panes = result || []; render(); $('connection').textContent = 'Desktop connected';
      if ($('notice').dataset.source === 'refresh') notice();
    } catch (error) {
      if (started !== generation) return;
      $('connection').textContent = 'Server unavailable'; notice(error.message); $('notice').dataset.source = 'refresh';
    } finally { refreshing = false; }
  }
  async function action(path, button) {
    button.disabled = true;
    try { await api(path, 'POST'); notice(); await refresh(); setTimeout(refresh, 600); }
    catch (error) { notice(error.message); $('notice').dataset.source = 'action'; }
    finally { button.disabled = false; render(); }
  }
  for (const [id, direction] of [['split-right-btn', 'right'], ['split-down-btn', 'bottom']]) $(id).onclick = () => {
    const p = panes.find(p => p.id === activeID); if (p?.wez_pane_id >= 0) action(`/api/split?wez_pane_id=${encodeURIComponent(p.wez_pane_id)}&dir=${direction}`, $(id));
  };
  for (const id of ['new-tab-btn', 'empty-new-tab']) $(id).onclick = () => {
    const current = panes.find(p => p.id === activeID);
    action('/api/spawn' + (current?.wez_pane_id >= 0 ? '?wez_pane_id=' + encodeURIComponent(current.wez_pane_id) : ''), $(id));
  };
  $('fit-toggle').onclick = () => {
    fit = !fit;
    if (fit) for (const inst of instances.values()) inst.desktop = desktopSize(inst.meta);
    for (const inst of instances.values()) resize(inst, !fit); render();
  };
  $('auth-toggle').onclick = () => { $('auth-form').hidden = !$('auth-form').hidden; if (!$('auth-form').hidden) $('token').focus(); };
  $('auth-form').onsubmit = event => {
    event.preventDefault(); token = $('token').value; generation++;
    try { sessionStorage.setItem('wezrelay_token', token); } catch (_) {}
    for (const inst of instances.values()) dispose(inst);
    panes = []; render(); $('auth-form').hidden = true; notice();
    if (refreshing) setTimeout(refresh, 200); else refresh();
  };
  $('ctrl-btn').onclick = () => { ctrl = !ctrl; $('ctrl-btn').setAttribute('aria-pressed', String(ctrl)); instances.get(activeID)?.term.focus(); };
  const keys = { escape: '\x1b', tab: '\t', backtab: '\x1b[Z', shiftenter: '\x1b\r', up: '\x1b[A', down: '\x1b[B', left: '\x1b[D', right: '\x1b[C', interrupt: '\x03', eof: '\x04', clear: '\x0c', enter: '\r' };
  document.querySelectorAll('[data-key]').forEach(button => {
    button.addEventListener('pointerdown', event => event.preventDefault());
    button.onclick = () => { const inst = instances.get(activeID); send(inst, keys[button.dataset.key]); inst?.term.focus(); };
  });
  const layout = () => {
    const viewport = window.visualViewport;
    for (const inst of instances.values()) inst.preserveViewport();
    if (viewport) {
      document.body.style.height = `${viewport.height}px`;
      document.body.style.top = `${viewport.offsetTop}px`;
      document.body.classList.toggle('compact-viewport', viewport.height < 500);
    }
    for (const inst of instances.values()) resize(inst);
  };
  window.addEventListener('resize', layout); window.visualViewport?.addEventListener('resize', layout);
  window.visualViewport?.addEventListener('scroll', layout);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
  render(); layout(); refresh(); setInterval(refresh, 2500);
})();
