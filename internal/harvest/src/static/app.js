/**
 * AG Batch Login - Frontend Application
 * Handles WebSocket, UI controls, file upload, and progress display
 */

// --- State ---
let ws = null;
let accounts = [];
let isConnected = false;

// --- DOM Elements ---
// NOTE: Older revisions of this UI shipped SQL export buttons + a modal.
// Those have been removed from index.html (we ship JSON + a "copy results"
// button now). Querying for the dead IDs would return null and break the
// page on first addEventListener call — so this map only references DOM
// nodes the current HTML actually defines.
const elements = {
    // Input
    textarea: document.getElementById('accountsTextarea'),
    fileInput: document.getElementById('fileInput'),
    loadBtn: document.getElementById('loadBtn'),
    clearBtn: document.getElementById('clearBtn'),
    accountCount: document.getElementById('accountCount'),
    // Controls
    startBtn: document.getElementById('startBtn'),
    pauseBtn: document.getElementById('pauseBtn'),
    stopBtn: document.getElementById('stopBtn'),
    retryBtn: document.getElementById('retryBtn'),
    headlessToggle: document.getElementById('headlessToggle'),
    concurrencySelect: document.getElementById('concurrencySelect'),
    // Progress
    progressSection: document.getElementById('progressSection'),
    progressText: document.getElementById('progressText'),
    progressPercent: document.getElementById('progressPercent'),
    progressFill: document.getElementById('progressFill'),
    elapsedTime: document.getElementById('elapsedTime'),
    successCount: document.getElementById('successCount'),
    failedCount: document.getElementById('failedCount'),
    activeCount: document.getElementById('activeCount'),
    // Log
    logSection: document.getElementById('logSection'),
    logContainer: document.getElementById('logContainer'),
    // Results
    resultsSection: document.getElementById('resultsSection'),
    resultsBody: document.getElementById('resultsBody'),
    importProxyBtn: document.getElementById('importProxyBtn'),
    exportJsonBtn: document.getElementById('exportJsonBtn'),
    copyResultsBtn: document.getElementById('copyResultsBtn'),
};

// --- WebSocket ---

function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${protocol}//${window.location.host}/ws`);

    ws.onopen = () => {
        isConnected = true;
        console.log('WebSocket connected');
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        updateUI(data);
    };

    ws.onclose = () => {
        isConnected = false;
        console.log('WebSocket disconnected, reconnecting...');
        setTimeout(connectWebSocket, 2000);
    };

    ws.onerror = (error) => {
        console.error('WebSocket error:', error);
    };
}

// --- UI Updates ---

function updateUI(status) {
    const { state, total, processed, success, failed, active_workers, elapsed, accounts: accountStatuses } = status;

    // Show sections
    if (total > 0) {
        elements.progressSection.style.display = 'block';
        elements.logSection.style.display = 'block';
        elements.resultsSection.style.display = 'block';
    }

    // Progress
    const percent = total > 0 ? Math.round((processed / total) * 100) : 0;
    elements.progressText.textContent = `${processed}/${total}`;
    elements.progressPercent.textContent = `${percent}%`;
    elements.progressFill.style.width = `${percent}%`;
    elements.elapsedTime.textContent = formatTime(elapsed);
    elements.successCount.textContent = success;
    elements.failedCount.textContent = failed;
    elements.activeCount.textContent = active_workers;

    // Controls state
    const isRunning = state === 'running' || state === 'retrying';
    const isPaused = state === 'paused';
    const isDone = state === 'done';

    elements.startBtn.disabled = isRunning || isPaused;
    elements.pauseBtn.disabled = !isRunning;
    elements.pauseBtn.textContent = isPaused ? 'Resume' : 'Pause';
    elements.stopBtn.disabled = !isRunning && !isPaused;
    elements.retryBtn.disabled = !isDone || failed === 0;
    elements.exportJsonBtn.disabled = success === 0;
    if (elements.copyResultsBtn) elements.copyResultsBtn.disabled = success === 0;
    if (elements.importProxyBtn) elements.importProxyBtn.disabled = success === 0;

    // Update results table
    if (accountStatuses) {
        updateResultsTable(accountStatuses);
        updateLog(accountStatuses);
    }
}

function updateResultsTable(accountStatuses) {
    const rows = [];
    let index = 1;

    for (const [email, info] of Object.entries(accountStatuses)) {
        const statusClass = info.status;
        const statusLabel = info.status.charAt(0).toUpperCase() + info.status.slice(1);
        const time = info.time ? `${info.time}s` : '-';
        const detail = info.detail || '-';

        rows.push(`
            <tr>
                <td>${index}</td>
                <td>${escapeHtml(email)}</td>
                <td><span class="status-badge ${statusClass}">${statusLabel}</span></td>
                <td>${escapeHtml(detail)}</td>
                <td>${time}</td>
            </tr>
        `);
        index++;
    }

    elements.resultsBody.innerHTML = rows.join('');
}

function updateLog(accountStatuses) {
    const logEntries = [];

    for (const [email, info] of Object.entries(accountStatuses)) {
        if (info.status === 'running' || info.status === 'success' || info.status === 'failed') {
            const statusClass = info.status === 'success' ? 'status-success' : 
                               info.status === 'failed' ? 'status-failed' : '';
            logEntries.push(
                `<div class="log-entry"><span class="email">${escapeHtml(email)}</span> - <span class="${statusClass}">${escapeHtml(info.detail || info.status)}</span></div>`
            );
        }
    }

    // Show last 50 entries
    elements.logContainer.innerHTML = logEntries.slice(-50).join('');
    elements.logContainer.scrollTop = elements.logContainer.scrollHeight;
}

// --- Event Handlers ---

// File upload
elements.fileInput.addEventListener('change', async (e) => {
    const file = e.target.files[0];
    if (!file) return;

    const text = await file.text();
    elements.textarea.value = text;
    updateAccountCount();
});

// Clear
elements.clearBtn.addEventListener('click', () => {
    elements.textarea.value = '';
    updateAccountCount();
});

// Load accounts
elements.loadBtn.addEventListener('click', async () => {
    const text = elements.textarea.value.trim();
    if (!text) {
        alert('Please paste or import accounts first');
        return;
    }

    try {
        const resp = await fetch('/api/accounts', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ text }),
        });

        const data = await resp.json();
        if (!resp.ok) {
            alert(data.error || 'Failed to load accounts');
            return;
        }

        accounts = data.accounts;
        elements.accountCount.textContent = `${data.count} accounts loaded`;
        elements.startBtn.disabled = false;
    } catch (err) {
        alert(`Error: ${err.message}`);
    }
});

// Start
elements.startBtn.addEventListener('click', async () => {
    const concurrency = parseInt(elements.concurrencySelect.value);
    const headless = elements.headlessToggle.checked;
    const provider = document.getElementById('providerSelect').value;

    try {
        await fetch('/api/start', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ provider, concurrency, headless }),
        });
    } catch (err) {
        alert(`Error starting batch: ${err.message}`);
    }
});

// Pause / Resume
elements.pauseBtn.addEventListener('click', async () => {
    const isPaused = elements.pauseBtn.textContent === 'Resume';
    const endpoint = isPaused ? '/api/resume' : '/api/pause';

    try {
        await fetch(endpoint, { method: 'POST' });
    } catch (err) {
        console.error(err);
    }
});

// Stop
elements.stopBtn.addEventListener('click', async () => {
    if (!confirm('Are you sure you want to stop the batch?')) return;

    try {
        await fetch('/api/stop', { method: 'POST' });
    } catch (err) {
        console.error(err);
    }
});

// Retry
elements.retryBtn.addEventListener('click', async () => {
    try {
        await fetch('/api/retry', { method: 'POST' });
    } catch (err) {
        alert(`Error: ${err.message}`);
    }
});

// Export JSON
elements.exportJsonBtn.addEventListener('click', () => {
    window.location.href = '/api/export/json';
});

// Import to LIAM Proxy: posts every successful result to the proxy's
// /api/accounts/import/ag endpoint. Useful when running the standalone
// harvest UI (port 8000) against a separate LIAM proxy instance — the
// embedded harvest in the dashboard already auto-imports as results land.
if (elements.importProxyBtn) {
    elements.importProxyBtn.addEventListener('click', async () => {
        const proxyUrl = prompt('LIAM proxy URL:', 'http://localhost:8080');
        if (!proxyUrl) return;
        const original = elements.importProxyBtn.textContent;
        elements.importProxyBtn.disabled = true;
        elements.importProxyBtn.textContent = 'Importing...';
        try {
            const resp = await fetch('/api/import', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ proxy_url: proxyUrl }),
            });
            const data = await resp.json();
            if (!resp.ok) {
                alert(`Import failed: ${data.error || 'unknown'}`);
            } else {
                alert(`Imported ${data.imported} of ${data.imported + data.failed} accounts. ${data.failed > 0 ? `Failed: ${data.failed}` : ''}`);
            }
        } catch (err) {
            alert(`Import error: ${err.message}`);
        } finally {
            elements.importProxyBtn.textContent = original;
            elements.importProxyBtn.disabled = false;
        }
    });
}

// Copy results: pulls /api/results, formats as `email:access_token:refresh_token`
// per line, and dumps to clipboard. This is the format the LIAM proxy's
// /api/accounts/import/ag endpoint expects when bulk-importing manually.
if (elements.copyResultsBtn) {
    elements.copyResultsBtn.addEventListener('click', async () => {
        try {
            const resp = await fetch('/api/results');
            const data = await resp.json();
            if (!resp.ok || !data.results || data.results.length === 0) {
                alert('No results to copy yet');
                return;
            }
            // Compact format: each line is a JSON blob ready to feed into
            // /api/accounts/import/ag. Shows email, refresh_token, project_id.
            const lines = data.results.map(r => JSON.stringify({
                email: r.email,
                access_token: r.credentials?.access_token || '',
                refresh_token: r.credentials?.refresh_token || '',
                project_id: r.credentials?.project_id || '',
            }));
            await navigator.clipboard.writeText(lines.join('\n'));
            const original = elements.copyResultsBtn.textContent;
            elements.copyResultsBtn.textContent = `Copied ${lines.length}!`;
            setTimeout(() => { elements.copyResultsBtn.textContent = original; }, 2000);
        } catch (err) {
            alert(`Copy failed: ${err.message}`);
        }
    });
}

// Textarea account count
elements.textarea.addEventListener('input', updateAccountCount);

function updateAccountCount() {
    const text = elements.textarea.value.trim();
    if (!text) {
        elements.accountCount.textContent = '0 accounts loaded';
        return;
    }
    const lines = text.split('\n').filter(l => l.trim() && l.includes(':'));
    elements.accountCount.textContent = `${lines.length} accounts detected`;
}

// --- Utilities ---

function formatTime(seconds) {
    if (seconds < 60) return `${Math.round(seconds)}s`;
    const mins = Math.floor(seconds / 60);
    const secs = Math.round(seconds % 60);
    return `${mins}m ${secs}s`;
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// --- Init ---
connectWebSocket();
