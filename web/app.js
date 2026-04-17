// Cookie helpers
function setCookie(name, value, days) {
    const expires = new Date(Date.now() + days * 864e5).toUTCString();
    document.cookie = name + '=' + encodeURIComponent(value) + ';expires=' + expires + ';path=/;SameSite=Lax';
}

function getCookie(name) {
    const match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
    return match ? decodeURIComponent(match[1]) : null;
}

function clearCookie(name) {
    document.cookie = name + '=;expires=Thu, 01 Jan 1970 00:00:00 GMT;path=/;SameSite=Lax';
}

// Token model
// ------------
// webSession            — long-lived per-browser session credential. Stored
//                         in localStorage / cookie. Sent as Bearer on every
//                         API call. Issued by /pingclaw/auth/verify-code.
// apiKeyPlaintext       — in-memory ONLY. Holds the api_key plaintext from
//                         the moment the user clicks "Rotate API Key" until
//                         the page is reloaded. Never persisted.
// pairingTokenPlaintext — same idea for pairing_token.
// hasApiKey,
// hasPairingToken       — booleans from GET /pingclaw/auth/me telling us
//                         whether the server has an active token of that
//                         kind. Drives the **** vs "Generate" rendering.

function saveSession() {
    if (webSession) {
        localStorage.setItem('web_session', webSession);
        setCookie('web_session', webSession, 90);
    }
}

function clearSession() {
    localStorage.removeItem('web_session');
    clearCookie('web_session');
    // Also clean up any leftover state from the previous token model so
    // nothing stale leaks across sessions.
    localStorage.removeItem('api_key');
    localStorage.removeItem('pairing_token');
    clearCookie('api_key');
    clearCookie('pairing_token');
}

let webSession = localStorage.getItem('web_session') || getCookie('web_session');
if (webSession && !localStorage.getItem('web_session')) {
    localStorage.setItem('web_session', webSession);
}

let apiKeyPlaintext = null;
let pairingTokenPlaintext = null;
let hasApiKey = false;
let hasPairingToken = false;
let webhookURL = null;
let webhookSecret = null;
let setupAutoStateApplied = false; // first fetchLocation decides; user toggle then takes over
let mcpTarget = localStorage.getItem('mcp_target') || 'code'; // 'code' | 'desktop'
let phoneNumber = '';

// Platform detection
const isIOS = /iPad|iPhone|iPod/.test(navigator.userAgent);
const isAndroid = /Android/.test(navigator.userAgent);
const platformLabel = isIOS ? 'iOS App' : isAndroid ? 'Android App' : '';

// Init
document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('.platform-tag').forEach(el => {
        if (platformLabel) {
            el.textContent = platformLabel;
        } else {
            el.style.display = 'none';
        }
    });

    document.querySelectorAll('.ios-only').forEach(el => {
        el.style.display = isIOS ? '' : 'none';
    });

    if (webSession) {
        showSection('dashboard');
    } else {
        showSection('landing');
    }
    updateHeaderBtn();
});

// Navigation
let locationPollInterval = null;

function showSection(id) {
    document.querySelectorAll('main > section').forEach(s => s.hidden = true);
    document.getElementById(id).hidden = false;
    updateHeaderBtn();

    if (id === 'dashboard') {
        loadDashboard();
        startLocationPolling();
    } else {
        stopLocationPolling();
    }
}

function startLocationPolling() {
    stopLocationPolling();
    locationPollInterval = setInterval(fetchLocation, 10000);
}

function stopLocationPolling() {
    if (locationPollInterval) {
        clearInterval(locationPollInterval);
        locationPollInterval = null;
    }
}

function handleHeaderBtn() {
    if (webSession) {
        logout();
    } else {
        showSection('auth');
    }
}

function updateHeaderBtn() {
    document.querySelectorAll('.header-btn').forEach(btn => {
        if (webSession) {
            btn.textContent = 'Sign out';
            btn.className = 'pc-btn-ghost pc-btn-sm header-btn';
        } else {
            btn.textContent = 'Sign in';
            btn.className = 'pc-btn-primary pc-btn-sm header-btn';
        }
    });
}

function normalizePhone(input) {
    let digits = input.replace(/[^\d+]/g, '');
    if (digits.startsWith('+')) return digits;
    digits = digits.replace(/\D/g, '');
    if (digits.length === 10) return '+1' + digits;
    if (digits.length === 11 && digits.startsWith('1')) return '+' + digits;
    if (digits.length >= 7) return '+' + digits;
    return null;
}

// Auth
async function sendCode() {
    const raw = document.getElementById('phone').value.trim();
    const errorEl = document.getElementById('phone-error');
    errorEl.hidden = true;

    const phone = normalizePhone(raw);
    if (!phone) {
        errorEl.textContent = 'Enter a valid phone number (e.g. 415-555-1234 or +14155551234)';
        errorEl.hidden = false;
        return;
    }
    phoneNumber = phone;

    try {
        const res = await fetch('/pingclaw/auth/send-code', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ phone_number: phone }),
        });
        const data = await res.json();
        if (!res.ok) {
            errorEl.textContent = data.error || 'Failed to send code';
            errorEl.hidden = false;
            return;
        }
        document.getElementById('phone-step').hidden = true;
        document.getElementById('code-step').hidden = false;
    } catch (err) {
        errorEl.textContent = 'Network error. Is the server running?';
        errorEl.hidden = false;
    }
}

async function verifyCode() {
    const code = document.getElementById('code').value.trim();
    const errorEl = document.getElementById('code-error');
    errorEl.hidden = true;

    if (!code) {
        errorEl.textContent = 'Enter the 6-digit code';
        errorEl.hidden = false;
        return;
    }

    try {
        const res = await fetch('/pingclaw/auth/verify-code', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ phone_number: phoneNumber, code }),
        });
        const data = await res.json();
        if (!res.ok) {
            errorEl.textContent = data.error || 'Verification failed';
            errorEl.hidden = false;
            return;
        }

        // Sign-in only issues a web_session for THIS browser. The api_key
        // and pairing_token (if any) are never touched here — the user
        // mints/rotates them explicitly from the dashboard.
        webSession = data.web_session;
        saveSession();

        document.getElementById('phone-step').hidden = false;
        document.getElementById('code-step').hidden = true;
        document.getElementById('phone').value = '';
        document.getElementById('code').value = '';

        showSection('dashboard');
    } catch (err) {
        errorEl.textContent = 'Network error. Is the server running?';
        errorEl.hidden = false;
    }
}

function logout() {
    webSession = null;
    apiKeyPlaintext = null;
    pairingTokenPlaintext = null;
    hasApiKey = false;
    hasPairingToken = false;
    webhookURL = null;
    webhookSecret = null;
    clearSession();
    showSection('landing');
}

// Dashboard
async function loadDashboard() {
    // Reset the "View my data" panel so it starts collapsed on every
    // fresh entry into the dashboard (sign-in, page reload, etc).
    const myDataWrap = document.getElementById('my-data-wrap');
    const myDataDisplay = document.getElementById('my-data-display');
    const myDataToggle = document.getElementById('my-data-toggle');
    if (myDataWrap) myDataWrap.style.display = 'none';
    if (myDataDisplay) {
        myDataDisplay.textContent = '';
        delete myDataDisplay.dataset.loaded;
    }
    if (myDataToggle) myDataToggle.textContent = 'View my data';

    // Setup sub-sections all start collapsed; the first fetchLocation will
    // expand the iOS one iff the user has no location data yet. Content
    // for each is fetched lazily on first expand.
    setupAutoStateApplied = false;
    SETUP_SUBS.forEach(name => setSetupSubExpanded(name, false));

    // Fetch token presence from the server. The dashboard needs to know
    // whether to show "**** • Rotate" or a "Generate" CTA, but the actual
    // plaintext values are never sent down by the server.
    try {
        const res = await fetch('/pingclaw/auth/me', {
            headers: { Authorization: 'Bearer ' + webSession },
        });
        if (res.status === 401) { logout(); return; }
        const data = await res.json();
        hasApiKey = !!data.has_api_key;
        hasPairingToken = !!data.has_pairing_token;
    } catch (err) {
        // Render anyway with whatever we know.
    }

    // Fetch the configured webhook (if any). Server returns plaintext URL
    // and secret because the user supplied them.
    try {
        const res = await fetch('/pingclaw/webhook', {
            headers: { Authorization: 'Bearer ' + webSession },
        });
        if (res.ok) {
            const data = await res.json();
            webhookURL = data.url || null;
            webhookSecret = data.webhook_secret || null;
        }
    } catch (err) { /* leave nulls */ }

    renderApiKey();
    renderPairingToken();
    renderMcpConfig();
    renderWebhook();
    fetchLocation();
}

function renderWebhook() {
    const empty = document.getElementById('webhook-empty');
    const configured = document.getElementById('webhook-configured');
    const urlInput = document.getElementById('webhook-url-display');
    const secretInput = document.getElementById('webhook-secret-display');
    if (!empty || !configured || !urlInput || !secretInput) return;

    if (webhookURL) {
        urlInput.value = webhookURL;
        secretInput.value = maskSecretTail(webhookSecret);
        configured.style.display = '';
        empty.style.display = 'none';
    } else {
        configured.style.display = 'none';
        empty.style.display = '';
    }
}

// Render only the last 4 characters of a secret, prefixed with bullets.
// The full value is still copyable via the Copy button.
function maskSecretTail(s) {
    if (!s) return '';
    const tail = s.length > 4 ? s.slice(-4) : s;
    return '\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022' + tail;
}

function toggleWebhookHelp() {
    const panel = document.getElementById('webhook-help');
    const label = document.getElementById('webhook-help-toggle-label');
    const curl  = document.getElementById('webhook-help-curl');
    if (!panel || !label || !curl) return;

    if (panel.style.display === 'none') {
        // Render the curl command lazily, using the live server origin.
        curl.textContent = renderWebhookCurl(window.location.origin);
        panel.style.display = '';
        label.textContent = 'Hide webhook instructions';
    } else {
        panel.style.display = 'none';
        label.textContent = 'Show webhook instructions';
    }
}

function renderWebhookCurl(origin) {
    return [
        'curl -X PUT ' + origin + '/pingclaw/webhook \\',
        '  -H "Authorization: Bearer $PINGCLAW_TOKEN" \\',
        '  -H "Content-Type: application/json" \\',
        "  -d '{\"url\":\"https://your-receiver.example.com/location\",\"secret\":\"'\"$WEBHOOK_SECRET\"'\"}'",
    ].join('\n');
}

// Copies the value of the input that immediately precedes the clicked button.
function copyAdjacentInput(btn) {
    const input = btn.previousElementSibling;
    if (!input || input.tagName !== 'INPUT') return;
    copyToClipboard(input.value, btn);
}

function copyWebhookCurl(btn) {
    const pre = document.getElementById('webhook-help-curl');
    if (!pre || !pre.textContent) return;
    copyToClipboard(pre.textContent, btn);
}

async function testWebhook() {
    const status = document.getElementById('webhook-test-status');
    if (status) {
        status.style.display = '';
        status.style.color = 'var(--pc-text-3)';
        status.textContent = 'Sending';
    }
    try {
        const res = await fetch('/pingclaw/webhook/test', {
            method: 'POST',
            headers: { Authorization: 'Bearer ' + webSession },
        });
        const data = await res.json();
        if (!status) return;
        if (res.ok) {
            const loc = data.location;
            const locStr = loc ? loc.lat.toFixed(4) + ', ' + loc.lng.toFixed(4) : 'unknown';
            status.style.color = 'var(--pc-accent)';
            status.textContent = 'Delivered (HTTP ' + data.delivered_status + ') · location ' + locStr;
        } else {
            status.style.color = 'var(--pc-error)';
            status.textContent = 'Failed: ' + (data.error || 'unknown error');
        }
    } catch (err) {
        if (status) {
            status.style.color = 'var(--pc-error)';
            status.textContent = 'Network error';
        }
    }
}

async function deleteWebhook() {
    if (!confirm('Remove the registered webhook? PingClaw will stop forwarding location updates until a new one is registered.')) return;
    try {
        const res = await fetch('/pingclaw/webhook', {
            method: 'DELETE',
            headers: { Authorization: 'Bearer ' + webSession },
        });
        if (!res.ok) { alert('Failed to remove webhook'); return; }
        webhookURL = null;
        webhookSecret = null;
        renderWebhook();
    } catch (err) {
        alert('Network error');
    }
}

function copyWebhookField(which) {
    const text = which === 'url' ? webhookURL : webhookSecret;
    if (!text) return;
    copyToClipboard(text, event.target);
}

// Render a token field. Three possible states:
//   1. Plaintext in memory (just rotated this session) — show real value
//      with Copy / iOS deep-link / Rotate.
//   2. Server has a token but we don't have plaintext locally — show ●●●●
//      with Rotate (which warns it kicks the existing client).
//   3. Server has no token yet — show a single "Generate" CTA.
const MASK = '\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022';

function renderApiKey() {
    const input = document.getElementById('api-key-display');
    const inputRow = document.getElementById('api-key-input-row');
    const actions = document.getElementById('api-key-actions');
    const empty = document.getElementById('api-key-empty');
    const copyBtn = document.getElementById('api-key-copy');
    const rotateBtn = document.getElementById('api-key-rotate');
    if (!input || !inputRow || !actions || !empty || !copyBtn || !rotateBtn) return;

    if (apiKeyPlaintext) {
        input.value = apiKeyPlaintext;
        inputRow.style.display = '';
        actions.style.display = '';
        empty.style.display = 'none';
        copyBtn.style.display = '';
        rotateBtn.textContent = 'Rotate';
    } else if (hasApiKey) {
        input.value = MASK;
        inputRow.style.display = '';
        actions.style.display = '';
        empty.style.display = 'none';
        copyBtn.style.display = 'none';
        rotateBtn.textContent = 'Rotate';
    } else {
        inputRow.style.display = 'none';
        actions.style.display = 'none';
        empty.style.display = '';
    }
}

function renderPairingToken() {
    const input = document.getElementById('pairing-token-display');
    const inputRow = document.getElementById('pairing-token-input-row');
    const actions = document.getElementById('pairing-token-actions');
    const empty = document.getElementById('pairing-token-empty');
    const copyBtn = document.getElementById('pairing-token-copy');
    const rotateBtn = document.getElementById('pairing-token-rotate');
    const pairBtn = document.getElementById('pairing-token-pair-app');
    if (!input || !inputRow || !actions || !empty || !copyBtn || !rotateBtn || !pairBtn) return;

    if (pairingTokenPlaintext) {
        input.value = pairingTokenPlaintext;
        inputRow.style.display = '';
        actions.style.display = '';
        empty.style.display = 'none';
        copyBtn.style.display = '';
        if (isIOS) pairBtn.style.display = '';
        rotateBtn.textContent = 'Rotate';
    } else if (hasPairingToken) {
        input.value = MASK;
        inputRow.style.display = '';
        actions.style.display = '';
        empty.style.display = 'none';
        copyBtn.style.display = 'none';
        pairBtn.style.display = 'none';
        rotateBtn.textContent = 'Rotate';
    } else {
        inputRow.style.display = 'none';
        actions.style.display = 'none';
        pairBtn.style.display = 'none';
        empty.style.display = '';
    }
}

// MCP_TARGETS describes each supported client: the per-paste intro line
// and a builder that returns the JSON snippet to render.
//
// The shared HTTP shape is { type: 'http', url, headers } — most clients
// accept it under either `mcpServers` (Claude/Cursor/Windsurf) or
// `servers` (VS Code). Claude Desktop is the outlier: stdio-only, wrapped
// in `npx mcp-remote`. Zed uses its own `context_servers` key.
const MCP_TARGETS = {
    'code': {
        label: 'Claude Code',
        intro: 'Paste this into ~/.claude.json (under "projects".<your-project>) and reconnect.',
        build: (url, token) => ({
            mcpServers: { pingclaw: { type: 'http', url, headers: { Authorization: 'Bearer ' + token } } },
        }),
    },
    'desktop': {
        label: 'Claude Desktop',
        intro: 'Paste this into ~/Library/Application Support/Claude/claude_desktop_config.json. Claude Desktop runs npx to bridge HTTP to its stdio transport.',
        build: (url, token) => ({
            mcpServers: {
                pingclaw: {
                    command: 'npx',
                    args: ['-y', 'mcp-remote', url, '--header', 'Authorization:Bearer ' + token],
                },
            },
        }),
    },
    'vscode': {
        label: 'VS Code',
        intro: 'Paste this into .vscode/mcp.json (workspace) or open the Command Palette → "MCP: Open User Configuration".',
        build: (url, token) => ({
            servers: { pingclaw: { type: 'http', url, headers: { Authorization: 'Bearer ' + token } } },
        }),
    },
    'cursor': {
        label: 'Cursor',
        intro: 'Paste this into ~/.cursor/mcp.json (or the workspace .cursor/mcp.json).',
        build: (url, token) => ({
            mcpServers: { pingclaw: { type: 'http', url, headers: { Authorization: 'Bearer ' + token } } },
        }),
    },
    'windsurf': {
        label: 'Windsurf',
        intro: 'Paste this into ~/.codeium/windsurf/mcp_config.json.',
        build: (url, token) => ({
            mcpServers: { pingclaw: { type: 'http', url, headers: { Authorization: 'Bearer ' + token } } },
        }),
    },
    'zed': {
        label: 'Zed',
        intro: 'Paste this into your Zed settings.json under "context_servers".',
        build: (url, token) => ({
            context_servers: { pingclaw: { type: 'http', url, headers: { Authorization: 'Bearer ' + token } } },
        }),
    },
};

function setMcpTarget(target) {
    mcpTarget = MCP_TARGETS[target] ? target : 'code';
    localStorage.setItem('mcp_target', mcpTarget);
    syncMcpTargetSelect();
    renderMcpConfig();
}

function syncMcpTargetSelect() {
    const sel = document.getElementById('mcp-target-select');
    if (sel && sel.value !== mcpTarget) sel.value = mcpTarget;
}

function renderMcpConfig() {
    const block = document.getElementById('claude-config');
    const copyBtn = document.getElementById('mcp-config-copy');
    const hint = document.getElementById('mcp-config-hint');
    const intro = document.getElementById('mcp-config-intro');
    const picker = document.getElementById('mcp-target-picker');
    if (!block || !copyBtn || !hint || !intro) return;

    if (hasApiKey) {
        const tokenDisplay = apiKeyPlaintext || MASK;
        const url = window.location.origin + '/pingclaw/mcp';
        const t = MCP_TARGETS[mcpTarget] || MCP_TARGETS['code'];

        intro.textContent = t.intro;
        block.textContent = JSON.stringify(t.build(url, tokenDisplay), null, 2);
        block.style.display = '';
        intro.style.display = '';
        copyBtn.style.display = '';
        if (picker) picker.style.display = '';
        syncMcpTargetSelect();
        if (apiKeyPlaintext) {
            hint.style.display = 'none';
        } else {
            hint.style.display = '';
            hint.textContent = 'Click Rotate next to your API Key to reveal the full token before copying.';
        }
    } else {
        block.style.display = 'none';
        copyBtn.style.display = 'none';
        intro.style.display = 'none';
        if (picker) picker.style.display = 'none';
        hint.style.display = '';
        hint.textContent = 'Generate an API Key above to create your MCP config.';
    }
}

// Setup is split into independent sub-sections (iOS, ChatGPT, MCP). Each
// fetches its markdown fragment from /setup/<name>.html lazily on first
// expand and caches the rendered HTML in the DOM thereafter.
const SETUP_SUBS = ['ios', 'chatgpt', 'mcp'];
const SETUP_LABELS = {
    'ios':     'iOS app setup',
    'chatgpt': 'ChatGPT integration',
    'mcp':     'MCP configuration',
};

async function loadSetupSubContent(name) {
    const el = document.getElementById('setup-' + name + '-content');
    if (!el || el.dataset.loaded === '1') return;
    try {
        const res = await fetch('/setup/' + name + '.html');
        if (!res.ok) return;
        el.innerHTML = await res.text();
        el.dataset.loaded = '1';
    } catch (err) { /* leave empty if fetch fails — non-critical */ }
}

function setSetupSubExpanded(name, expanded) {
    const content = document.getElementById('setup-' + name + '-content');
    const label = document.getElementById('setup-' + name + '-label');
    if (!content || !label) return;
    if (expanded) {
        loadSetupSubContent(name);
        content.style.display = '';
        label.textContent = 'Hide ' + SETUP_LABELS[name];
    } else {
        content.style.display = 'none';
        label.textContent = 'Show ' + SETUP_LABELS[name];
    }
}

function toggleSetupSub(name) {
    const content = document.getElementById('setup-' + name + '-content');
    if (!content) return;
    setSetupSubExpanded(name, content.style.display === 'none');
    setupAutoStateApplied = true; // user has interacted; don't auto-toggle anymore
}

async function fetchLocation() {
    const el = document.getElementById('location-status');
    if (!el.dataset.loaded) el.textContent = 'Locating';

    try {
        const res = await fetch('/pingclaw/location', {
            headers: { Authorization: 'Bearer ' + webSession },
        });
        if (res.status === 401) {
            el.textContent = 'Session expired. Signing out.';
            setTimeout(logout, 1500);
            return;
        }
        const data = await res.json();
        if (data.status === 'no_location') {
            el.innerHTML = 'Open PingClaw on your phone to start sharing your location with your agent.';
            // First-time decision: no location → expand the iOS sub-section so
            // the user sees the steps to start sharing.
            if (!setupAutoStateApplied) {
                setSetupSubExpanded('ios', true);
                setupAutoStateApplied = true;
            }
            return;
        }
        // First-time decision: location is present → leave all sub-sections collapsed.
        if (!setupAutoStateApplied) {
            setupAutoStateApplied = true;
        }
        const loc = data.location;
        const serverNow = new Date(data.server_time).getTime();
        const locationTime = new Date(data.timestamp).getTime();
        const age = Math.max(0, Math.round((serverNow - locationTime) / 1000));
        const ageStr = age < 10 ? 'Updated just now'
            : age < 60 ? 'Updated ' + age + 's ago'
            : age < 3600 ? 'Updated ' + Math.round(age / 60) + 'm ago'
            : age < 86400 ? 'Updated ' + Math.round(age / 3600) + 'h ago'
            : 'Updated ' + Math.round(age / 86400) + 'd ago';
        const mapsUrl = isIOS
            ? 'maps://?q=' + loc.lat + ',' + loc.lng
            : 'https://www.google.com/maps?q=' + loc.lat + ',' + loc.lng;
        const latStr = formatCoord(loc.lat, true);
        const lngStr = formatCoord(loc.lng, false);
        const acc = Math.round(loc.accuracy_metres);
        const activity = data.activity || 'Located';
        el.innerHTML =
            '<div class="pc-location-readout" style="border:none; padding:0; background:transparent;">' +
                '<div class="pc-location-label">' + escapeHtml(activity) + '</div>' +
                '<div class="pc-location-coords">' +
                    '<span>' + latStr + '</span>' +
                    '<span class="pc-location-sep"> · </span>' +
                    '<span>' + lngStr + '</span>' +
                '</div>' +
                '<div class="pc-location-accuracy">± ' + acc + 'm · ' + ageStr + '</div>' +
            '</div>' +
            '<div style="display:flex;justify-content:flex-end;margin-top:16px;">' +
                '<a href="' + mapsUrl + '" target="_blank" class="pc-btn-ghost pc-btn-sm">Show map</a>' +
            '</div>';
        el.dataset.loaded = '1';
    } catch (err) {
        el.textContent = 'Location unavailable.';
    }
}

function formatCoord(value, isLat) {
    const hemi = isLat ? (value >= 0 ? 'N' : 'S') : (value >= 0 ? 'E' : 'W');
    return Math.abs(value).toFixed(4) + '° ' + hemi;
}

function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => ({
        '&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;',
    }[c]));
}

// Mint or rotate the pairing_token. If one already exists on the server it
// is revoked first, so any phone currently using it stops working.
async function rotatePairingToken() {
    if (hasPairingToken) {
        if (!confirm('Generate a new pairing token? Your currently paired phone will stop sending location updates until you re-paste the new token.')) return;
    }
    try {
        const res = await fetch('/pingclaw/auth/rotate-pairing-token', {
            method: 'POST',
            headers: { Authorization: 'Bearer ' + webSession },
        });
        const data = await res.json();
        if (!res.ok) { alert(data.error || 'Failed to issue token'); return; }
        pairingTokenPlaintext = data.pairing_token;
        hasPairingToken = true;
        renderPairingToken();
        flashField('pairing-token-display');
    } catch (err) {
        alert('Network error');
    }
}

async function rotateAPIKey() {
    if (hasApiKey) {
        if (!confirm('Rotate your API Key? Your MCP agent config will stop working until you paste in the new key.')) return;
    }
    try {
        const res = await fetch('/pingclaw/auth/rotate-api-key', {
            method: 'POST',
            headers: { Authorization: 'Bearer ' + webSession },
        });
        const data = await res.json();
        if (!res.ok) { alert(data.error || 'Failed to rotate'); return; }
        apiKeyPlaintext = data.api_key;
        hasApiKey = true;
        renderApiKey();
        renderMcpConfig();
        flashField('api-key-display');
    } catch (err) {
        alert('Network error');
    }
}

function flashField(id) {
    const el = document.getElementById(id);
    if (!el) return;
    el.style.transition = 'background-color 0.3s, border-color 0.3s';
    el.style.backgroundColor = 'var(--pc-accent-bg)';
    el.style.borderColor = 'var(--pc-accent)';
    setTimeout(() => {
        el.style.backgroundColor = '';
        el.style.borderColor = '';
        setTimeout(() => el.style.transition = '', 500);
    }, 750);
}

async function viewMyData() {
    const wrap = document.getElementById('my-data-wrap');
    const display = document.getElementById('my-data-display');
    const toggle = document.getElementById('my-data-toggle');
    if (!wrap || !display) return;
    if (wrap.style.display !== 'none' && display.dataset.loaded === '1') {
        // Toggle off if already shown.
        wrap.style.display = 'none';
        if (toggle) toggle.textContent = 'View my data';
        return;
    }
    wrap.style.display = '';
    display.textContent = 'Loading…';
    if (toggle) toggle.textContent = 'Hide my data';
    try {
        const res = await fetch('/pingclaw/auth/data', {
            headers: { Authorization: 'Bearer ' + webSession },
        });
        const data = await res.json();
        if (!res.ok) {
            display.textContent = 'Failed: ' + (data.error || res.status);
            return;
        }
        display.textContent = JSON.stringify(data, null, 2);
        display.dataset.loaded = '1';
    } catch (err) {
        display.textContent = 'Network error';
    }
}

async function deleteAccount() {
    if (!confirm('Are you sure? This will permanently delete your account and all data.')) return;
    if (!confirm('This cannot be undone. Delete your account?')) return;
    try {
        const res = await fetch('/pingclaw/auth/account', {
            method: 'DELETE',
            headers: { Authorization: 'Bearer ' + webSession },
        });
        if (!res.ok) { alert('Failed to delete account'); return; }
        logout();
    } catch (err) {
        alert('Network error');
    }
}

function openInApp() {
    if (!pairingTokenPlaintext) return;
    window.location.href = 'pingclaw://pair?token=' + encodeURIComponent(pairingTokenPlaintext);
}

function copyToken(type) {
    const text = type === 'pairing' ? pairingTokenPlaintext : apiKeyPlaintext;
    if (!text) return;
    copyToClipboard(text, event.target);
}

function copyConfig() {
    const text = document.getElementById('claude-config').textContent;
    if (!text) return;
    copyToClipboard(text, event.target);
}

function copyToClipboard(text, btn) {
    if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(() => showCopied(btn));
    } else {
        const textarea = document.createElement('textarea');
        textarea.value = text;
        textarea.style.position = 'fixed';
        textarea.style.opacity = '0';
        document.body.appendChild(textarea);
        textarea.focus();
        textarea.select();
        document.execCommand('copy');
        document.body.removeChild(textarea);
        showCopied(btn);
    }
}

function showCopied(btn) {
    const original = btn.textContent;
    btn.textContent = 'Copied!';
    setTimeout(() => btn.textContent = original, 1500);
}

document.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    if (e.target.id === 'phone') sendCode();
    if (e.target.id === 'code') verifyCode();
});
