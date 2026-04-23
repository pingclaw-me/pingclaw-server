// PingClaw — shared site JS

(function () {
  'use strict';

  // ---------- dashboard: collapsible prose ----------

  window.toggleProse = function (el) {
    el.parentElement.classList.toggle('open');
  };

  // ---------- dashboard: rail active state on scroll ----------

  function wireRailScroll() {
    const railItems = document.querySelectorAll('.rail-item');
    if (!railItems.length) return;

    const sections = Array.from(document.querySelectorAll('.dash-section'));
    if (!sections.length) return;

    function updateRail() {
      const scrollY = window.scrollY + 120;
      let activeId = sections[0].id;
      for (const s of sections) {
        if (s.offsetTop <= scrollY) activeId = s.id;
      }
      railItems.forEach(item => {
        item.classList.toggle('active', item.getAttribute('href') === '#' + activeId);
      });
    }

    window.addEventListener('scroll', updateRail, { passive: true });
    updateRail();
  }

  // ---------- hero readout: live-feeling age counter ----------

  function wireHeroReadout() {
    const metaRow = document.querySelector('.hero .meta-row');
    if (!metaRow) return;

    const ageSpan = Array.from(metaRow.querySelectorAll('span')).find(s =>
      s.textContent.trim().toLowerCase().startsWith('age')
    );
    if (!ageSpan) return;

    let s = 4;
    setInterval(() => {
      s++;
      if (s > 59) s = 1;
      ageSpan.innerHTML = '<b>age</b> ' + s + 's';
    }, 1000);
  }

  // ---------- copy buttons: toggle label ----------

  function wireCopyButtons() {
    document.querySelectorAll('.snippet-copy, [data-copy-btn]').forEach(btn => {
      btn.addEventListener('click', () => {
        var snippet = btn.closest('.snippet');
        var text = '';
        if (snippet) {
          var body = snippet.querySelector('.snippet-body');
          if (body) text = body.textContent;
        }
        if (!text) {
          var block = btn.closest('.card, .dash-section');
          if (block) {
            var code = block.querySelector('.code-block');
            if (code) text = code.textContent;
          }
        }
        if (text) {
          if (navigator.clipboard && window.isSecureContext) {
            navigator.clipboard.writeText(text);
          } else {
            var ta = document.createElement('textarea');
            ta.value = text;
            ta.style.position = 'fixed';
            ta.style.opacity = '0';
            document.body.appendChild(ta);
            ta.select();
            document.execCommand('copy');
            document.body.removeChild(ta);
          }
        }
        var orig = btn.textContent;
        btn.textContent = 'Copied';
        setTimeout(function () { btn.textContent = orig; }, 1400);
      });
    });
  }

  // ---------- landing: agent grid → snippet switcher ----------

  function wireAgentGrid() {
    const grid = document.getElementById('agent-grid');
    const fileEl = document.getElementById('agent-snippet-file');
    const bodyEl = document.getElementById('agent-snippet-body');
    if (!grid || !fileEl || !bodyEl) return;

    var k = function (s) { return '<span class="key">"' + s + '"</span>'; };
    var s = function (v) { return '<span class="str">"' + v + '"</span>'; };
    var c = function (v) { return '<span class="com">' + v + '</span>'; };

    var url = 'https://pingclaw.me/pingclaw/mcp';
    var tok = 'ak_…';

    var agents = {
      chatgpt: {
        file: 'chatgpt.com/g/pingclaw',
        body: c('// no config needed — just open the GPT') + '\n' +
              c('// ChatGPT handles auth via OAuth automatically') + '\n\n' +
              'Open the PingClaw GPT:\n' +
              s('https://chatgpt.com/g/g-69e2d7fe797c8191ab8c0356f375daca-pingclaw'),
      },
      code: {
        file: '~/.claude/mcp.json',
        body: c('// one line, and your agent knows where you are') + '\n{\n' +
              '  ' + k('mcpServers') + ': {\n' +
              '    ' + k('pingclaw') + ': {\n' +
              '      ' + k('url') + ': ' + s(url) + ',\n' +
              '      ' + k('headers') + ': { ' + k('Authorization') + ': ' + s('Bearer ' + tok) + ' }\n' +
              '    }\n  }\n}',
      },
      desktop: {
        file: '~/Library/Application Support/Claude/claude_desktop_config.json',
        body: c('// Claude Desktop uses npx mcp-remote to bridge HTTP to stdio') + '\n{\n' +
              '  ' + k('mcpServers') + ': {\n' +
              '    ' + k('pingclaw') + ': {\n' +
              '      ' + k('command') + ': ' + s('npx') + ',\n' +
              '      ' + k('args') + ': [' + s('-y') + ', ' + s('mcp-remote') + ',\n' +
              '        ' + s(url) + ',\n' +
              '        ' + s('--header') + ', ' + s('Authorization:Bearer ' + tok) + ']\n' +
              '    }\n  }\n}',
      },
      cursor: {
        file: '~/.cursor/mcp.json',
        body: c('// drop this in and reconnect') + '\n{\n' +
              '  ' + k('mcpServers') + ': {\n' +
              '    ' + k('pingclaw') + ': {\n' +
              '      ' + k('url') + ': ' + s(url) + ',\n' +
              '      ' + k('headers') + ': { ' + k('Authorization') + ': ' + s('Bearer ' + tok) + ' }\n' +
              '    }\n  }\n}',
      },
      zed: {
        file: 'Zed settings.json',
        body: c('// add under "context_servers"') + '\n{\n' +
              '  ' + k('context_servers') + ': {\n' +
              '    ' + k('pingclaw') + ': {\n' +
              '      ' + k('url') + ': ' + s(url) + ',\n' +
              '      ' + k('headers') + ': { ' + k('Authorization') + ': ' + s('Bearer ' + tok) + ' }\n' +
              '    }\n  }\n}',
      },
      vscode: {
        file: '.vscode/mcp.json',
        body: c('// workspace or user MCP config') + '\n{\n' +
              '  ' + k('servers') + ': {\n' +
              '    ' + k('pingclaw') + ': {\n' +
              '      ' + k('url') + ': ' + s(url) + ',\n' +
              '      ' + k('headers') + ': { ' + k('Authorization') + ': ' + s('Bearer ' + tok) + ' }\n' +
              '    }\n  }\n}',
      },
      openclaw: {
        file: '~/.openclaw/openclaw.json',
        body: c('// enable hooks, then register via the dashboard') + '\n{\n' +
              '  ' + k('hooks') + ': {\n' +
              '    ' + k('enabled') + ': true,\n' +
              '    ' + k('token') + ': ' + s('your-hook-token') + ',\n' +
              '    ' + k('path') + ': ' + s('/hooks') + '\n' +
              '  }\n}',
      },
      webhook: {
        file: '$ terminal',
        body: c('# register a webhook with your API key') + '\n' +
              'curl -X PUT https://pingclaw.me/pingclaw/webhook \\\n' +
              '  -H ' + s('Authorization: Bearer ' + tok) + ' \\\n' +
              '  -H ' + s('Content-Type: application/json') + ' \\\n' +
              '  -d \'{"url":"https://your-receiver/location","secret":"your-secret"}\'',
      },
    };

    grid.addEventListener('click', function (e) {
      var cell = e.target.closest('[data-agent]');
      if (!cell) return;
      var agent = cell.dataset.agent;
      var info = agents[agent];
      if (!info) return;

      grid.querySelectorAll('.client-cell').forEach(function (el) {
        el.classList.toggle('active', el === cell);
      });
      fileEl.textContent = info.file;
      bodyEl.innerHTML = info.body;
    });
  }

  // ---------- landing: sign-in form ----------

  function wireSigninForm() {
    const input = document.getElementById('signin-code');
    const submit = document.getElementById('signin-submit');
    const hint = document.getElementById('signin-hint');
    if (!input || !submit) return;

    const defaultHint = '8 characters, letters and numbers. The middle dot is added automatically.';

    // Focus input when Sign In nav link is clicked
    document.querySelectorAll('a[href="#signin"]').forEach(function (link) {
      link.addEventListener('click', function () {
        setTimeout(function () { input.focus(); }, 400);
      });
    });

    function formatValue(raw) {
      const clean = raw.replace(/[^A-Za-z0-9]/g, '').toUpperCase().slice(0, 8);
      if (clean.length <= 4) return clean;
      return clean.slice(0, 4) + ' \u00B7 ' + clean.slice(4);
    }

    function cleanLength(val) {
      return val.replace(/[^A-Za-z0-9]/g, '').length;
    }

    input.addEventListener('input', () => {
      // Remember caret position relative to clean chars
      const sel = input.selectionStart;
      const beforeCaret = input.value.slice(0, sel).replace(/[^A-Za-z0-9]/g, '').length;

      const formatted = formatValue(input.value);
      input.value = formatted;

      // Restore caret
      let cleanSeen = 0;
      let newPos = 0;
      for (let i = 0; i < formatted.length; i++) {
        if (/[A-Za-z0-9]/.test(formatted[i])) cleanSeen++;
        if (cleanSeen >= beforeCaret) { newPos = i + 1; break; }
      }
      if (beforeCaret === 0) newPos = 0;
      input.setSelectionRange(newPos, newPos);

      submit.disabled = cleanLength(input.value) !== 8;

      // Clear error state on new input
      input.classList.remove('error');
      if (hint) {
        hint.classList.remove('error');
        hint.textContent = defaultHint;
      }
    });

    input.addEventListener('paste', (e) => {
      e.preventDefault();
      const pasted = (e.clipboardData || window.clipboardData).getData('text');
      input.value = formatValue(pasted);
      submit.disabled = cleanLength(input.value) !== 8;
    });

    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !submit.disabled) {
        e.preventDefault();
        submit.click();
      }
    });

    submit.addEventListener('click', async (e) => {
      e.preventDefault();
      const cleaned = input.value.replace(/[^A-Za-z0-9]/g, '').toUpperCase();
      if (cleaned.length !== 8) return;

      submit.disabled = true;
      submit.textContent = 'Signing in\u2026';

      try {
        const res = await fetch('/pingclaw/auth/web-login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ code: cleaned }),
        });

        if (res.ok) {
          const data = await res.json();
          if (data.web_session) {
            // Store session and reload to render dashboard
            localStorage.setItem('web_session', data.web_session);
            document.cookie = 'web_session=' + encodeURIComponent(data.web_session) + ';path=/;SameSite=Lax;max-age=' + (90 * 86400);
            window.location.href = '/';
          }
        } else {
          const err = await res.json().catch(() => ({}));
          input.classList.add('error');
          if (hint) {
            hint.classList.add('error');
            hint.textContent = err.error || 'Code not recognized. Generate a new one from your phone.';
          }
          submit.disabled = false;
          submit.textContent = 'Sign in';
        }
      } catch (err) {
        if (hint) {
          hint.classList.add('error');
          hint.textContent = 'Network error. Is the server running?';
        }
        submit.disabled = false;
        submit.textContent = 'Sign in';
      }
    });
  }

  // ---------- init ----------

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  function init() {
    wireRailScroll();
    wireHeroReadout();
    wireCopyButtons();
    wireAgentGrid();
    wireSigninForm();
  }
})();
