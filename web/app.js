(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);

  const loginEl = $('login');
  const termEl = $('term-screen');
  const host = $('terminal');
  const pwEl = $('password');
  const errEl = $('login-error');
  const statusDot = $('status-dot');
  const statusText = $('status-text');

  let term = null;
  let fit = null;
  let ws = null;
  let reconnectTimer = 0;
  let attempts = 0;
  let loggedIn = false;
  let ctrl = false;
  let alt = false;
  let fontSize = clampFont(parseInt(localStorage.getItem('rt.font') || '14', 10));

  function clampFont(n) {
    if (!Number.isFinite(n)) n = 14;
    return Math.min(22, Math.max(9, n));
  }

  // ---------------------------------------------------------------- viewport

  function syncViewport() {
    const vv = window.visualViewport;
    const h = vv ? vv.height : window.innerHeight;
    document.documentElement.style.setProperty('--app-height', Math.round(h) + 'px');
    scheduleFit();
  }

  let fitTimer = 0;
  function scheduleFit() {
    clearTimeout(fitTimer);
    fitTimer = setTimeout(fitNow, 80);
  }

  function fitNow() {
    if (!term || !fit || termEl.classList.contains('hidden')) return;
    const cols = term.cols;
    const rows = term.rows;
    try {
      fit.fit();
    } catch (e) {
      return;
    }
    if (term.cols !== cols || term.rows !== rows) sendResize();
  }

  function sendResize() {
    if (!term || !ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
  }

  // ---------------------------------------------------------------- terminal

  function initTerminal() {
    if (term) return;

    term = new Terminal({
      fontSize,
      fontFamily: '"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      lineHeight: 1.15,
      letterSpacing: 0,
      cursorBlink: true,
      cursorStyle: 'bar',
      scrollback: 5000,
      allowProposedApi: true,
      macOptionIsMeta: true,
      rightClickSelectsWord: true,
      theme: {
        background: '#0b0d10',
        foreground: '#d7dde4',
        cursor: '#7ee787',
        cursorAccent: '#0b0d10',
        selectionBackground: '#264f78',
        black: '#1c2128',
        red: '#f47067',
        green: '#7ee787',
        yellow: '#e3b341',
        blue: '#6cb6ff',
        magenta: '#dcbdfb',
        cyan: '#56d4dd',
        white: '#cdd9e5',
        brightBlack: '#636e7b',
        brightRed: '#ff938a',
        brightGreen: '#a2f5a9',
        brightYellow: '#f0c674',
        brightBlue: '#8cc8ff',
        brightMagenta: '#e6c8ff',
        brightCyan: '#7ce7ee',
        brightWhite: '#ffffff',
      },
    });

    fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(host);
    term.onData(sendInput);
    term.onResize(() => sendResize());

    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true;
      const k = ev.key.toLowerCase();
      if (ev.ctrlKey && ev.shiftKey && k === 'c') {
        const sel = term.getSelection();
        if (sel) {
          navigator.clipboard.writeText(sel).catch(() => {});
          return false;
        }
      }
      if (ev.ctrlKey && ev.shiftKey && k === 'v') {
        navigator.clipboard.readText().then((t) => t && sendRaw(t)).catch(() => {});
        return false;
      }
      return true;
    });

    applyFont();
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(() => scheduleFit());
    }
  }

  function applyFont() {
    if (!term) return;
    term.options.fontSize = fontSize;
    try {
      localStorage.setItem('rt.font', String(fontSize));
    } catch (e) {}
    scheduleFit();
  }

  // ------------------------------------------------------------------ input

  const KEY_SEQ = {
    esc: '\x1b',
    tab: '\t',
    enter: '\r',
    ctrlc: '\x03',
    up: '\x1b[A',
    down: '\x1b[B',
    right: '\x1b[C',
    left: '\x1b[D',
  };

  function sendRaw(s) {
    if (!s) return;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(new TextEncoder().encode(s));
  }

  function sendInput(data) {
    if (!data) return;
    if (ctrl) {
      if (data.length === 1) {
        const c = data.toUpperCase().charCodeAt(0);
        if (c >= 64 && c <= 95) data = String.fromCharCode(c - 64);
        else if (c === 32) data = '\x00';
      }
      ctrl = false;
      syncMods();
    }
    if (alt) {
      data = '\x1b' + data;
      alt = false;
      syncMods();
    }
    sendRaw(data);
  }

  function syncMods() {
    $('ctrl-key').classList.toggle('on', ctrl);
    $('alt-key').classList.toggle('on', alt);
  }

  // --------------------------------------------------------------- websocket

  function connect() {
    if (!loggedIn) return;
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) return;

    clearTimeout(reconnectTimer);
    reconnectTimer = 0;
    setStatus('connecting', 'warn');

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    let sock;
    try {
      sock = new WebSocket(proto + '//' + location.host + '/ws');
    } catch (e) {
      scheduleReconnect();
      return;
    }
    sock.binaryType = 'arraybuffer';
    ws = sock;

    sock.onopen = () => {
      if (ws !== sock) return;
      attempts = 0;
      setStatus('connected', 'ok');
      fitNow();
      sendResize();
    };

    sock.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        onControl(ev.data);
        return;
      }
      if (term) term.write(new Uint8Array(ev.data));
    };

    sock.onclose = () => {
      if (ws !== sock) return;
      ws = null;
      setStatus('disconnected', 'bad');
      scheduleReconnect();
    };

    sock.onerror = () => {};
  }

  function scheduleReconnect() {
    if (!loggedIn || reconnectTimer) return;
    attempts++;
    const delay = Math.min(400 * Math.pow(1.7, Math.min(attempts, 8)), 8000);
    reconnectTimer = setTimeout(async () => {
      reconnectTimer = 0;
      if (!loggedIn) return;
      const me = await api('/api/me');
      if (me === null) {
        scheduleReconnect();
        return;
      }
      if (!me.authenticated) {
        showLogin('session expired - enter a new password from your computer');
        return;
      }
      connect();
    }, delay);
  }

  function onControl(raw) {
    let msg;
    try {
      msg = JSON.parse(raw);
    } catch (e) {
      return;
    }
    if (msg.type === 'exit') showEnded(msg.code);
  }

  function showEnded(code) {
    loggedIn = false;
    if (ws) {
      try { ws.close(); } catch (e) {}
      ws = null;
    }
    const box = $('ended');
    box.querySelector('p').textContent = code
      ? 'session ended (exit code ' + code + ')'
      : 'session ended';
    box.classList.remove('hidden');
    setStatus('ended', 'bad');
  }

  function setStatus(text, kind) {
    statusText.textContent = text;
    statusDot.className = 'dot' + (kind === 'ok' ? ' ok' : kind === 'bad' ? ' bad' : '');
  }

  async function api(path, opts) {
    try {
      const r = await fetch(path, opts);
      if (!r.ok) return null;
      return await r.json();
    } catch (e) {
      return null;
    }
  }

  // ------------------------------------------------------------- screen flow

  function showTerminal() {
    loggedIn = true;
    errEl.textContent = '';
    loginEl.classList.add('hidden');
    termEl.classList.remove('hidden');
    $('ended').classList.add('hidden');
    syncViewport();
    initTerminal();
    requestAnimationFrame(() => {
      syncViewport();
      fitNow();
      if (term) term.focus();
      connect();
    });
  }

  function showLogin(message) {
    loggedIn = false;
    if (ws) {
      try { ws.close(); } catch (e) {}
      ws = null;
    }
    clearTimeout(reconnectTimer);
    reconnectTimer = 0;
    termEl.classList.add('hidden');
    loginEl.classList.remove('hidden');
    errEl.textContent = message || '';
    if (term) term.blur();
    setTimeout(() => pwEl.focus(), 60);
  }

  // ---------------------------------------------------------------- handlers

  $('login-form').addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const pw = pwEl.value.trim();
    if (!pw) {
      errEl.textContent = 'enter the password from your computer';
      return;
    }
    errEl.textContent = '';
    const btn = $('login-btn');
    btn.disabled = true;
    try {
      const r = await fetch('/api/auth', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: pw }),
      });
      let data = {};
      try { data = await r.json(); } catch (e) {}
      if (!r.ok) {
        errEl.textContent = data.error || 'wrong password';
        pwEl.select();
        return;
      }
      pwEl.value = '';
      showTerminal();
    } catch (e) {
      errEl.textContent = 'network error - is the tunnel still running?';
    } finally {
      btn.disabled = false;
    }
  });

  document.querySelectorAll('#keys [data-send]').forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      ev.preventDefault();
      sendRaw(KEY_SEQ[btn.dataset.send] || '');
      if (term) term.focus();
    });
  });

  document.querySelectorAll('#keys [data-mod]').forEach((btn) => {
    btn.addEventListener('click', (ev) => {
      ev.preventDefault();
      if (btn.dataset.mod === 'ctrl') ctrl = !ctrl;
      else alt = !alt;
      syncMods();
    });
  });

  $('paste').addEventListener('click', async (ev) => {
    ev.preventDefault();
    try {
      const t = await navigator.clipboard.readText();
      if (t) sendRaw(t);
    } catch (e) {
      if (term) term.focus();
    }
  });

  $('kbd').addEventListener('click', (ev) => {
    ev.preventDefault();
    if (term) term.focus();
  });

  $('font-up').addEventListener('click', () => {
    fontSize = clampFont(fontSize + 1);
    applyFont();
  });

  $('font-down').addEventListener('click', () => {
    fontSize = clampFont(fontSize - 1);
    applyFont();
  });

  $('reconnect').addEventListener('click', () => {
    attempts = 0;
    if (ws) {
      try { ws.close(); } catch (e) {}
      ws = null;
    }
    connect();
  });

  $('logout').addEventListener('click', async () => {
    await fetch('/api/logout', { method: 'POST' }).catch(() => {});
    showLogin('');
  });

  $('ended-close').addEventListener('click', () => {
    $('ended').classList.add('hidden');
    showLogin('the session on your computer has ended');
  });

  window.addEventListener('resize', syncViewport);
  window.addEventListener('orientationchange', () => setTimeout(syncViewport, 250));
  if (window.visualViewport) {
    window.visualViewport.addEventListener('resize', syncViewport);
    window.visualViewport.addEventListener('scroll', syncViewport);
  }
  if (window.ResizeObserver) {
    new ResizeObserver(() => scheduleFit()).observe(host);
  }

  host.addEventListener('click', () => {
    if (term) term.focus();
  });

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    syncViewport();
    if (loggedIn && (!ws || ws.readyState > WebSocket.OPEN)) {
      attempts = 0;
      connect();
    }
  });

  async function boot() {
    syncViewport();
    const me = await api('/api/me');
    if (me && me.authenticated) showTerminal();
    else showLogin('');
  }

  boot();
})();
