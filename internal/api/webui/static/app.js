/* joshbot webui.
 *
 * Talks to the existing OpenAI-compatible surface: POST /v1/chat/completions
 * with stream:true. There is no second chat path, and there is no credential in
 * this file, in the DOM or in localStorage — the browser holds an opaque
 * httpOnly cookie it cannot read, and the CSRF token lives in a page-lifetime
 * variable only.
 */
(function () {
  'use strict';

  var CSRF = '';          // per-session, page lifetime only. Never persisted.
  var CFG = {};
  var aborter = null;
  var inFlight = false;

  var $ = function (id) { return document.getElementById(id); };

  /* ------------------------------ theme ------------------------------ */
  var root = document.documentElement;
  var themes = ['dark', 'light', ''];   // '' follows the system preference
  function applyTheme(t) {
    if (t) root.setAttribute('data-theme', t); else root.removeAttribute('data-theme');
    try { localStorage.setItem('jb-theme', t); } catch (e) { /* private mode */ }
    var b = $('btn-theme');
    if (b) b.textContent = '◐ ' + (t === '' ? 'system' : t);
  }
  (function () {
    var saved = null;
    try { saved = localStorage.getItem('jb-theme'); } catch (e) { /* private mode */ }
    applyTheme(saved === null ? 'dark' : saved);
  })();
  function cycleTheme() {
    var cur = root.getAttribute('data-theme') || '';
    applyTheme(themes[(themes.indexOf(cur) + 1) % themes.length]);
  }

  /* --------------------------- session name ---------------------------
   * The server keys sessions "api:<user>". "New conversation" mints a new
   * <user>; it never deletes anything, because there is no delete route and
   * pretending otherwise would be a lie about where the data went.
   *
   * The charset and the 64-char cap mirror api.senderID exactly, so a name
   * this page mints is never rejected as a 400. */
  function newUser() {
    var b = new Uint8Array(4);
    (window.crypto || window.msCrypto).getRandomValues(b);
    var hex = '';
    for (var i = 0; i < b.length; i++) hex += ('0' + b[i].toString(16)).slice(-2);
    return 'web-' + hex;
  }
  function currentUser() {
    var u = null;
    try { u = localStorage.getItem('jb-user'); } catch (e) { /* private mode */ }
    if (!u || !/^[A-Za-z0-9._-]{1,64}$/.test(u)) {
      u = newUser();
      try { localStorage.setItem('jb-user', u); } catch (e) { /* private mode */ }
    }
    return u;
  }
  function rotateUser() {
    var u = newUser();
    try { localStorage.setItem('jb-user', u); } catch (e) { /* private mode */ }
    return u;
  }

  /* ---------------------------- rendering ----------------------------
   * Model output is untrusted: it is text a model wrote after reading the
   * web. Everything below builds text nodes and elements — there is no
   * innerHTML on model text anywhere in this file, and no markdown library,
   * which would be both a dependency and an XSS surface. */
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  // Inline pass: `code`, **bold**, and bare http(s) links. Everything else is
  // a text node.
  var INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(https?:\/\/[^\s<>()]+)/g;
  function inline(parent, text) {
    var last = 0, m;
    INLINE.lastIndex = 0;
    while ((m = INLINE.exec(text)) !== null) {
      if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
      if (m[1]) {
        parent.appendChild(el('code', null, m[1].slice(1, -1)));
      } else if (m[2]) {
        parent.appendChild(el('strong', null, m[2].slice(2, -2)));
      } else {
        var a = el('a', null, m[3]);
        a.href = m[3];
        a.target = '_blank';
        a.rel = 'noopener noreferrer';
        parent.appendChild(a);
      }
      last = m.index + m[0].length;
    }
    if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
  }

  function codeBlock(lang, body) {
    var box = el('div', 'code');
    var head = el('div', 'code-head');
    head.appendChild(el('span', 'lang', lang || 'text'));
    head.appendChild(el('span', 'spacer'));
    var copy = el('button', 'copy', 'Copy');
    copy.type = 'button';
    copy.addEventListener('click', function () {
      var done = function () {
        copy.classList.add('done');
        copy.textContent = '✓ Copied';
        setTimeout(function () { copy.classList.remove('done'); copy.textContent = 'Copy'; }, 1400);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(body).then(done, done);
      } else { done(); }
    });
    head.appendChild(copy);
    box.appendChild(head);
    var pre = el('pre');
    pre.appendChild(el('code', null, body));
    box.appendChild(pre);
    return box;
  }

  // Block pass: fenced code blocks split the text; everything between them is
  // paragraphs. An unterminated fence renders as code to the end, which is what
  // a half-streamed answer looks like mid-flight.
  function renderMarkdown(target, text) {
    while (target.firstChild) target.removeChild(target.firstChild);
    var parts = String(text).split(/```/);
    for (var i = 0; i < parts.length; i++) {
      if (i % 2 === 1) {
        var nl = parts[i].indexOf('\n');
        var lang = nl === -1 ? '' : parts[i].slice(0, nl).trim();
        var body = nl === -1 ? parts[i] : parts[i].slice(nl + 1);
        target.appendChild(codeBlock(lang, body.replace(/\n$/, '')));
        continue;
      }
      var prose = el('div', 'prose');
      var paras = parts[i].split(/\n{2,}/);
      var wrote = false;
      for (var j = 0; j < paras.length; j++) {
        if (paras[j].trim() === '') continue;
        var p = el('p');
        inline(p, paras[j].replace(/\n/g, ' '));
        prose.appendChild(p);
        wrote = true;
      }
      if (wrote) target.appendChild(prose);
    }
  }

  /* ----------------------------- transcript ----------------------------- */
  var logView, emptyView, scroller;

  function showLog() {
    emptyView.hidden = true;
    logView.hidden = false;
  }
  function clearLog() {
    while (logView.firstChild) logView.removeChild(logView.firstChild);
    logView.hidden = true;
    emptyView.hidden = false;
  }
  function atBottom() {
    return scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 80;
  }
  function pin(force) {
    if (force || atBottom()) scroller.scrollTop = scroller.scrollHeight;
  }

  function turn(kind, who) {
    showLog();
    var art = el('article', 'turn ' + kind);
    art.appendChild(el('div', 'gutter', kind === 'user' ? '❯' : (kind === 'err' ? '⨯' : '●')));
    var body = el('div', 'body');
    body.appendChild(el('div', 'who', who));
    art.appendChild(body);
    logView.appendChild(art);
    return body;
  }

  function addUser(text) {
    var body = turn('user', 'you');
    var bubble = el('div', 'bubble');
    var p = el('p');
    inline(p, text);
    bubble.appendChild(p);
    body.appendChild(bubble);
    pin(true);
  }

  function addAssistant(text) {
    var body = turn('bot', 'joshbot');
    var slot = el('div');
    renderMarkdown(slot, text);
    body.appendChild(slot);
    pin(true);
    return slot;
  }

  function addError(text) {
    var body = turn('err', 'error');
    var box = el('div', 'alert');
    box.setAttribute('role', 'alert');
    box.appendChild(el('div', 'h', '⨯ request failed'));
    box.appendChild(el('div', 'm', text));
    body.appendChild(box);
    pin(true);
  }

  /* ------------------------------ transport ------------------------------ */
  function api(path, opts) {
    opts = opts || {};
    opts.headers = opts.headers || {};
    opts.credentials = 'same-origin';
    // The CSRF header rides only the cookie path; a bearer client never needs
    // it. Sending it here is what makes a cross-origin form post — which cannot
    // set a custom header — insufficient on its own.
    if (opts.method && opts.method !== 'GET' && opts.method !== 'HEAD') {
      opts.headers['X-Joshbot-CSRF'] = CSRF;
    }
    return fetch(path, opts);
  }

  function boot() {
    return api('/webui/config').then(function (res) {
      if (res.status === 401) { showGate(); return; }
      if (!res.ok) throw new Error('config: HTTP ' + res.status);
      return res.json().then(function (cfg) {
        CFG = cfg;
        CSRF = cfg.csrf || '';
        showApp();
        return loadTranscript();
      });
    }).catch(function (err) {
      showGate(String(err && err.message ? err.message : err));
    });
  }

  function loadTranscript() {
    var user = currentUser();
    return api('/webui/session?user=' + encodeURIComponent(user)).then(function (res) {
      if (!res.ok) return;
      return res.json().then(function (data) {
        var msgs = (data && data.messages) || [];
        for (var i = 0; i < msgs.length; i++) {
          if (msgs[i].role === 'user') addUser(msgs[i].content);
          else addAssistant(msgs[i].content);
        }
        setStats(msgs.length);
        pin(true);
      });
    });
  }

  function setStats(turns) {
    $('stat-session').textContent = 'api:' + currentUser();
    $('stat-model').textContent = CFG.model || 'joshbot';
    $('stat-version').textContent = CFG.version || 'unknown';
    $('stat-endpoint').textContent = location.host;
    $('thread-sub').textContent = 'api:' + currentUser() +
      (turns ? ' · ' + turns + (turns === 1 ? ' message' : ' messages') : '');
  }

  /* -------------------------------- send -------------------------------- */
  function send(text) {
    if (inFlight) return;
    text = text.trim();
    if (text === '') return;

    addUser(text);
    inFlight = true;
    setSending(true);

    var body = turn('bot', 'joshbot');
    var slot = el('div');
    var caret = el('span', 'caret');
    body.appendChild(slot);
    body.appendChild(caret);
    var acc = '';
    var failed = false;
    var finished = false;

    aborter = new AbortController();

    api('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      signal: aborter.signal,
      body: JSON.stringify({
        model: CFG.model || 'joshbot',
        user: currentUser(),
        stream: true,
        messages: [{ role: 'user', content: text }]
      })
    }).then(function (res) {
      if (res.status === 401) { showGate('Session expired. Sign in again.'); throw new Error('unauthorized'); }
      // A failure before the first delta is an ordinary JSON error with a real
      // status — no SSE at all — so the status is checked before the body is
      // treated as a stream.
      if (!res.ok) {
        return res.text().then(function (t) { throw new Error(errText(t, res.status)); });
      }
      var reader = res.body.getReader();
      var dec = new TextDecoder();
      var buf = '';

      function pump() {
        return reader.read().then(function (r) {
          if (r.done) return;
          buf += dec.decode(r.value, { stream: true });
          var frames = buf.split('\n\n');
          buf = frames.pop();
          for (var i = 0; i < frames.length; i++) {
            var line = frames[i].trim();
            if (line.indexOf('data: ') !== 0) continue;
            var payload = line.slice(6);
            if (payload === '[DONE]') return;
            var obj;
            try { obj = JSON.parse(payload); } catch (e) { continue; }
            if (obj.error) {
              // Mid-stream failure: status was already 200. The absence of a
              // finish_reason "stop" frame is the signal the answer is partial.
              failed = true;
              addError(obj.error.message || 'the turn failed mid-stream');
              continue;
            }
            var ch = obj.choices && obj.choices[0];
            if (!ch) continue;
            if (ch.finish_reason === 'stop') finished = true;
            var d = ch.delta && ch.delta.content;
            if (d) {
              acc += d;
              renderMarkdown(slot, acc);
              pin(false);
            }
          }
          return pump();
        });
      }
      return pump();
    }).then(function () {
      if (!failed && !finished && acc === '') {
        addError('The server closed the stream without answering.');
      }
    }).catch(function (err) {
      if (err && err.name === 'AbortError') {
        body.appendChild(el('div', 'note', 'Stopped. This turn was cancelled and was not saved to the session.'));
        return;
      }
      if (!err || err.message !== 'unauthorized') {
        addError(String(err && err.message ? err.message : err));
      }
    }).then(function () {
      if (caret.parentNode) caret.parentNode.removeChild(caret);
      if (acc === '' && slot.parentNode && !failed) slot.parentNode.removeChild(slot);
      inFlight = false;
      aborter = null;
      setSending(false);
      setStats(logView.children.length);
      pin(false);
    });
  }

  function errText(bodyText, status) {
    try {
      var o = JSON.parse(bodyText);
      if (o && o.error && o.error.message) return o.error.message;
    } catch (e) { /* not JSON */ }
    return 'HTTP ' + status;
  }

  function setSending(on) {
    $('stopbar').hidden = !on;
    $('input').disabled = on;
    $('btn-send').disabled = on || $('input').value.trim() === '';
  }

  /* -------------------------------- views -------------------------------- */
  function showGate(msg) {
    $('app').hidden = true;
    $('gate').hidden = false;
    var e = $('login-error');
    if (msg) { e.textContent = msg; e.hidden = false; } else { e.hidden = true; }
    $('key').focus();
  }
  function showApp() {
    $('gate').hidden = true;
    $('app').hidden = false;
    setStats(0);
    $('input').focus();
  }

  /* -------------------------------- wiring -------------------------------- */
  document.addEventListener('DOMContentLoaded', function () {
    logView = $('log-view');
    emptyView = $('empty-view');
    scroller = $('scroll');

    $('login-form').addEventListener('submit', function (e) {
      e.preventDefault();
      var btn = $('btn-login');
      btn.disabled = true;
      fetch('/webui/login', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key: $('key').value })
      }).then(function (res) {
        if (!res.ok) {
          return res.text().then(function (t) {
            var e2 = $('login-error');
            e2.textContent = errText(t, res.status);
            e2.hidden = false;
          });
        }
        $('key').value = '';
        return boot();
      }).catch(function (err) {
        var e2 = $('login-error');
        e2.textContent = String(err && err.message ? err.message : err);
        e2.hidden = false;
      }).then(function () { btn.disabled = false; });
    });

    $('btn-logout').addEventListener('click', function () {
      api('/webui/logout', { method: 'POST' }).then(function () {
        CSRF = '';
        clearLog();
        showGate('Signed out.');
      });
    });

    $('btn-new').addEventListener('click', function () {
      if (inFlight) return;
      rotateUser();
      clearLog();
      setStats(0);
      setDrawer(false);
      $('input').focus();
    });

    var input = $('input');
    function grow() {
      input.style.height = 'auto';
      input.style.height = Math.min(input.scrollHeight, 200) + 'px';
      $('btn-send').disabled = inFlight || input.value.trim() === '';
    }
    input.addEventListener('input', grow);
    grow();
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault();
        var v = input.value;
        input.value = '';
        grow();
        send(v);
      }
    });
    $('composer').addEventListener('submit', function (e) {
      e.preventDefault();
      var v = input.value;
      input.value = '';
      grow();
      send(v);
    });

    $('btn-stop').addEventListener('click', function () { if (aborter) aborter.abort(); });

    Array.prototype.forEach.call(document.querySelectorAll('#chips .chip'), function (c) {
      c.addEventListener('click', function () {
        var t = c.querySelector('.t');
        input.value = t ? t.textContent : '';
        grow();
        input.focus();
      });
    });

    ['btn-theme', 'btn-theme-top'].forEach(function (id) {
      var b = $(id);
      if (b) b.addEventListener('click', cycleTheme);
    });

    $('btn-drawer').addEventListener('click', function () {
      setDrawer(!document.body.classList.contains('drawer'));
    });
    $('scrim').addEventListener('click', function () { setDrawer(false); });

    document.addEventListener('keydown', function (e) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); input.focus(); }
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === 'o') { e.preventDefault(); $('btn-new').click(); }
      if (e.key === 'Escape') {
        if (document.body.classList.contains('drawer')) { setDrawer(false); return; }
        if (aborter) aborter.abort();
      }
    });

    boot();
  });

  function setDrawer(open) {
    document.body.classList.toggle('drawer', open);
    $('scrim').hidden = !open;
    $('btn-drawer').setAttribute('aria-expanded', String(open));
  }
})();
