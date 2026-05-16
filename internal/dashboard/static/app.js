function app() {
  return {
    authenticated: false,
    authChecked: false,
    password: '',
    loginError: '',
    token: '',
    page: 'overview',
    sidebarCollapsed: false,
    navItems: [
      { id: 'overview', label: 'Overview', icon: 'dashboard' },
      { id: 'providers', label: 'Providers', icon: 'hub' },
      { id: 'combos', label: 'Combos', icon: 'merge_type' },
      { id: 'usage', label: 'Usage', icon: 'analytics' },
      { id: 'keys', label: 'Keys', icon: 'key' },
      { id: 'integrations', label: 'Integrations', icon: 'integration_instructions' },
      { id: 'harvest', label: 'Harvest', icon: 'agriculture' },
      { id: 'settings', label: 'Settings', icon: 'settings' },
    ],
    // Data
    stats: {},
    accounts: [],
    keys: [],
    providerStats: [],
    usageStats: {},
    recentUsage: [],
    chartData: [],
    liveRequests: [],
    overviewStats: [],

    // Bundled overview data fetched from /api/overview. Keeps the
    // homepage to a single round-trip per refresh instead of fanning
    // out to /stats + /providers/stats + /usage/recent + /sync/status.
    overview: {
      providers: [],
      stats: {},
      top_models: [],
      recent_errors: [],
      backoff_active: 0,
      api_keys_total: 0,
      sync: {},
    },
    overviewRefreshInterval: null,
    registryModels: [],
    integrations: [],

    // Provider detail
    providerDetail: null,
    providerAccounts: [],
    providerModels: [],
    testingModel: null,
    testResults: {},

    // Add Model
    showAddModelModal: false,
    newModel: { provider_alias: 'ag', model_id: '', display_name: '', type: 'llm' },
    newModelTestResult: null,
    newModelTesting: false,

    // Keys
    showCreateKeyModal: false,
    newKeyName: '',
    createdKey: null,
    keyVisibility: {},

    // Integrations (2-level navigation)
    integrationDetail: null, // null = grid view, "claude-code" = detail view
    integrationDetailData: null,
    integrationConfig: {
      base_url: '',
      api_key: '',
      models: {},
      agent_models: {},
      use_custom_url: false,
      custom_url: '',
    },
    integrationSnippet: '',
    integrationActionMsg: {},
    integrationApplyMsg: '',
    integrationApplyOk: null,

    // Base URL settings
    baseURL: '',
    defaultBaseURL: '',
    baseURLMsg: '',
    baseURLOk: null,
    syncStatus: { enabled: false, connected: false, last_sync: '', supabase_url: '' },

    // Combos
    combos: [],
    showComboModal: false,
    editingCombo: null,
    comboForm: { name: '', models: [], strategy: 'fallback', sticky_limit: 1 },

    // Routing
    routing: { strategy: 'round-robin', sticky_limit: 3, provider_overrides: {} },
    routingMsg: '',

    // Drag-and-drop
    dragAccountId: null,

    // Add Account
    showAddAccountModal: false,
    addAccountTab: 'oauth', // 'oauth' | 'token'
    addAccountForm: { refresh_token: '', email: '', callback_url: '' },
    addAccountOAuthURL: '',
    addAccountLoading: false,
    addAccountMsg: '',
    addAccountOk: null,
    addAccountReplaceId: '', // when set, /import endpoint updates that row in place

    // Edit Account
    showEditAccountModal: false,
    editAccountId: '',
    editAccountEmail: '',
    editAccountMsg: '',

    // Account auto-refresh — persisted in localStorage. Default ON: a fresh
    // user opening the dashboard expects quotas to update automatically. The
    // actual interval/countdown only starts after the first toggleAccountAutoRefresh()
    // is invoked from init() (so the timer respects the saved value).
    accountAutoRefresh: true,
    accountRefreshInterval: null,
    accountRefreshCountdown: 60,
    accountCountdownInterval: null,

    // Account test state (per-id)
    testingAccountId: null,
    accountTestResults: {}, // { [id]: { valid, error, latency_ms, refreshed, ts } }

    savedPresets: [],

    // Model Select Modal
    showModelSelectModal: false,
    modelSelectTarget: null, // {tool, slot} or 'addModel'
    modelSelectSearch: '',
    modelSelectCurrent: '',

    // Manual Config Modal
    showManualConfigModal: false,
    manualConfigContent: '',
    manualConfigPath: '',
    manualConfigTool: '',

    // Usage detail
    expandedUsageId: null,
    expandedUsageData: null,
    usageDetailOpen: false,
    usageSections: { request: true, response: true },
    usagePage: 0,
    usagePageSize: 10,

    // Usage period filter — drives both the stats card row and the chart.
    // Default "today" matches the previous behaviour. Persisted in
    // localStorage so a refresh keeps the user on the same window.
    usagePeriod: 'today',
    usagePeriods: [
      { id: 'today', label: 'Today' },
      { id: '24h',   label: '24h' },
      { id: '7d',    label: '7 days' },
      { id: '30d',   label: '30 days' },
      { id: '60d',   label: '60 days' },
    ],

    // Harvest
    harvest: { provider: 'ag', concurrency: '4', headless: 'true', accounts: '', filter: 'all' },
    harvestStatus: { running: false, success: 0, failed: 0, total: 0, active: 0, started_at: 0, ended_at: 0, logs: [], accounts: [] },
    harvestPoll: null,
    // Re-rendered every second to keep the elapsed/ETA labels updating even
    // when the status payload is unchanged (e.g. between 2-second polls).
    harvestNow: Math.floor(Date.now() / 1000),
    harvestNowTimer: null,

    // Settings
    settings: { currentPw: '', newPw: '', msg: '', ok: false },

    // UI
    copied: false,

    // Toast notifications + confirm dialog. Stored on the Alpine state so
    // every component can call `this.toast(...)` / `this.confirmDialog(...)`
    // and the rendered modal/stack lives in the page shell. The dashboard
    // used to lean on browser-native alert/confirm which is jarring and
    // hard to style; these helpers replace all 23 call sites with one
    // consistent UX.
    toasts: [],         // { id, kind: "info"|"success"|"error"|"warn", title, message }
    confirmState: null, // { title, message, confirmLabel, cancelLabel, danger, resolve }
    endpoint: '',
    chart: null,
    usageChart: null,
    sse: null,

    get harvestAccountCount() {
      if (!this.harvest.accounts) return 0;
      return this.harvest.accounts.split('\n').filter(l => l.trim() && l.includes(':')).length;
    },

    // Seconds since the run kicked off (or total wall-clock if it has ended).
    get harvestElapsed() {
      const s = this.harvestStatus;
      if (!s.started_at) return 0;
      const end = s.running ? this.harvestNow : (s.ended_at || this.harvestNow);
      return Math.max(0, end - s.started_at);
    },

    // Naive ETA: extrapolate from current throughput. We avoid showing
    // anything until at least one account has finished (the early signal
    // is noisy enough to mislead) and clamp to a sane upper bound.
    get harvestETA() {
      const s = this.harvestStatus;
      if (!s.running) return 0;
      const done = (s.success || 0) + (s.failed || 0);
      const remaining = (s.total || 0) - done;
      if (done < 1 || remaining <= 0) return 0;
      const elapsed = this.harvestElapsed;
      if (elapsed < 5) return 0;
      // Per-account average × remaining, capped at 6 hours so a single
      // hung worker can't make the dashboard claim "ETA: 47 hours".
      return Math.min(Math.round((elapsed / done) * remaining), 6 * 3600);
    },

    formatDuration(secs) {
      if (!secs || secs < 0) return '-';
      if (secs < 60) return `${secs}s`;
      const m = Math.floor(secs / 60);
      const s = secs % 60;
      if (m < 60) return `${m}m ${s}s`;
      const h = Math.floor(m / 60);
      return `${h}h ${m % 60}m`;
    },

    // Filter the per-account table by status. Empty filter = show all.
    get filteredHarvestAccounts() {
      const f = this.harvest.filter;
      if (!f || f === 'all') return this.harvestStatus.accounts || [];
      return (this.harvestStatus.accounts || []).filter(a => a.status === f);
    },

    get harvestProgressPercent() {
      const s = this.harvestStatus;
      if (!s.total) return 0;
      return Math.round(((s.success + s.failed) / s.total) * 100);
    },

    get groupedRegistryModels() {
      // Group by provider
      const groups = {};
      for (const m of this.registryModels) {
        if (!m.is_enabled) continue;
        const p = m.provider_alias || 'other';
        if (!groups[p]) groups[p] = [];
        groups[p].push(m);
      }
      return groups;
    },

    get filteredModelGroups() {
      const search = this.modelSelectSearch.toLowerCase();
      if (!search) return this.groupedRegistryModels;
      const filtered = {};
      for (const [provider, models] of Object.entries(this.groupedRegistryModels)) {
        const match = models.filter(m =>
          m.id.toLowerCase().includes(search) ||
          (m.display_name || '').toLowerCase().includes(search)
        );
        if (match.length > 0) filtered[provider] = match;
      }
      return filtered;
    },

    init() {
      this.endpoint = 'http://localhost:' + location.port + '/v1/chat/completions';
      this.token = localStorage.getItem('liam_token') || '';
      this.sidebarCollapsed = localStorage.getItem('liam_sidebar_collapsed') === '1';
      // Restore auto-refresh preference. Default ON when nothing is stored
      // so a fresh dashboard refreshes quotas automatically.
      const storedAR = localStorage.getItem('liam_account_auto_refresh');
      this.accountAutoRefresh = storedAR === null ? true : storedAR === '1';
      // Restore the user's last picked usage period.
      const storedPeriod = localStorage.getItem('liam_usage_period');
      if (storedPeriod && this.usagePeriods.some(p => p.id === storedPeriod)) {
        this.usagePeriod = storedPeriod;
      }
      this.loadPresets();
      // Bind URL hash routing so refreshing the browser keeps the user on
      // the same page/sub-page. Format: #/<page> or #/<page>/<detail>.
      // We restore the state from the hash now (in case the URL already
      // points at e.g. #/providers/kiro) and listen for hashchange so the
      // browser back/forward buttons keep state in sync.
      this.applyHashRoute();
      window.addEventListener('hashchange', () => this.applyHashRoute());
      if (this.token) {
        this.verifyToken();
      } else {
        this.authChecked = true;
      }
    },

    // ---- URL hash routing helpers ----
    // The Alpine state is the source of truth at runtime; the hash mirrors
    // it so a hard refresh restores the same view. We avoid a real router
    // because the dashboard is a single static HTML file served by Go.
    parseHash() {
      const raw = (window.location.hash || '').replace(/^#\/?/, '');
      if (!raw) return { page: 'overview' };
      const parts = raw.split('/').filter(Boolean);
      const page = parts[0] || 'overview';
      const detail = parts[1] ? decodeURIComponent(parts[1]) : '';
      return { page, detail };
    },
    applyHashRoute() {
      const { page, detail } = this.parseHash();
      const validPages = (this.navItems || []).map(n => n.id);
      if (!validPages.includes(page)) {
        // Unknown page: silently fall back to overview without polluting history.
        if (this.page !== 'overview') this.page = 'overview';
        return;
      }
      const pageChanged = this.page !== page;
      this.page = page;

      // Sub-page detail. We only auto-open known providers/integrations
      // because openProvider/openIntegrationDetail rely on data that is
      // only populated after loadAll(); when the user lands directly on
      // a deep link before loadAll, openSubpage() is called again from
      // loadAll's continuation in startSSE-friendly path.
      this._pendingSubpage = detail || '';

      if (pageChanged) {
        this.providerDetail = null;
        this.integrationDetail = null;
        this.expandedUsageId = null;
        this.usageDetailOpen = false;
      }

      this.$nextTick(() => {
        if (this.page === 'usage') this.renderUsageChart();
        if (this.page === 'integrations') this.fetchIntegrations();
        this.openSubpageFromHash();
      });
    },
    openSubpageFromHash() {
      const target = this._pendingSubpage;
      if (!target) return;
      if (this.page === 'providers') {
        // Wait until accounts list is populated (loadAll() finishes).
        if (!this.accounts || !this.accounts.length) return;
        if (this.providerDetail !== target) this.openProvider(target);
        this._pendingSubpage = '';
      } else if (this.page === 'integrations') {
        if (!this.integrations || !this.integrations.length) return;
        if (this.integrationDetail !== target) this.openIntegration(target);
        this._pendingSubpage = '';
      }
    },
    setHash(page, detail) {
      const next = detail ? `#/${page}/${encodeURIComponent(detail)}` : `#/${page}`;
      if (window.location.hash !== next) {
        // Use replaceState when only the detail changed within the same page
        // so browser history isn't cluttered with every provider switch.
        history.replaceState(null, '', next);
      }
    },
    navigateTo(page) {
      const next = `#/${page}`;
      if (window.location.hash !== next) {
        history.pushState(null, '', next);
        this.applyHashRoute();
      } else {
        // Same page click — still trigger onPageChange side effects.
        this.onPageChange();
      }
    },

    loadPresets() {
      try {
        const saved = localStorage.getItem('liam_endpoint_presets');
        if (saved) this.savedPresets = JSON.parse(saved);
      } catch (e) { this.savedPresets = []; }
    },

    // ---- Toast notifications ----
    // Replaces window.alert(...) across the dashboard. Returns the toast
    // id so callers can dismiss programmatically if needed.
    toast(message, opts = {}) {
      const id = Date.now() + Math.random();
      const kind = opts.kind || 'info';
      const t = {
        id,
        kind,
        title: opts.title || (kind === 'error' ? 'Error' : kind === 'success' ? 'Success' : kind === 'warn' ? 'Warning' : 'Info'),
        message: typeof message === 'string' ? message : String(message),
      };
      this.toasts.push(t);
      const ttl = opts.ttl ?? (kind === 'error' ? 8000 : 4000);
      setTimeout(() => this.dismissToast(id), ttl);
      return id;
    },
    dismissToast(id) {
      this.toasts = this.toasts.filter(t => t.id !== id);
    },

    // ---- Confirm dialog ----
    // Returns a Promise<boolean>. Replaces window.confirm(...).
    confirmDialog(message, opts = {}) {
      return new Promise(resolve => {
        this.confirmState = {
          title: opts.title || 'Are you sure?',
          message: typeof message === 'string' ? message : String(message),
          confirmLabel: opts.confirmLabel || (opts.danger ? 'Delete' : 'OK'),
          cancelLabel: opts.cancelLabel || 'Cancel',
          danger: !!opts.danger,
          resolve,
        };
      });
    },
    closeConfirm(answer) {
      if (this.confirmState && typeof this.confirmState.resolve === 'function') {
        this.confirmState.resolve(answer);
      }
      this.confirmState = null;
    },

    savePreset(url) {
      if (!url || this.savedPresets.includes(url)) return;
      this.savedPresets.push(url);
      localStorage.setItem('liam_endpoint_presets', JSON.stringify(this.savedPresets));
    },

    deletePreset(url) {
      this.savedPresets = this.savedPresets.filter(u => u !== url);
      localStorage.setItem('liam_endpoint_presets', JSON.stringify(this.savedPresets));
    },

    toggleSidebar() {
      this.sidebarCollapsed = !this.sidebarCollapsed;
      localStorage.setItem('liam_sidebar_collapsed', this.sidebarCollapsed ? '1' : '0');
    },

    // Auth
    async login() {
      this.loginError = '';
      try {
        const r = await fetch('/api/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: this.password }) });
        const d = await r.json();
        if (!r.ok) { this.loginError = d.error || 'Failed'; return; }
        this.token = d.token;
        localStorage.setItem('liam_token', d.token);
        this.authenticated = true;
        this.authChecked = true;
        this.password = '';
        this.loadAll();
      } catch (e) { this.loginError = 'Connection error'; }
    },
    async verifyToken() {
      try {
        const r = await fetch('/api/auth/verify', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: this.token }) });
        if (r.ok) { this.authenticated = true; this.loadAll(); }
        else { localStorage.removeItem('liam_token'); this.token = ''; }
      } catch (e) { this.authenticated = true; this.loadAll(); }
      finally { this.authChecked = true; }
    },
    logout() { localStorage.removeItem('liam_token'); this.token = ''; this.authenticated = false; if (this.sse) this.sse.close(); },

    // Data
    async loadAll() {
      await Promise.all([
        this.fetchStats(),
        this.fetchOverview(),
        this.fetchAccounts(),
        this.fetchKeys(),
        this.fetchProviders(),
        this.fetchUsageStats(),
        this.fetchRecentUsage(),
        this.fetchChart(),
        this.fetchRegistryModels(),
        this.fetchBaseURL(),
        this.fetchSyncStatus(),
        this.fetchCombos(),
        this.fetchRouting(),
      ]);
      this.buildOverviewStats();
      this.startSSE();
      this.startHarvestPoll();
      // Kick off page-specific handlers (Overview's polling + chart on
      // Usage). The chart on the old Overview page is gone now — we
      // bundle everything via /api/overview instead.
      this.onPageChange();
      // Initial data is now in memory — re-run the deep link handler so
      // refreshing on #/providers/kiro lands on that detail page.
      if (this._pendingSubpage) {
        if (this.page === 'integrations') {
          await this.fetchIntegrations();
        }
        this.$nextTick(() => this.openSubpageFromHash());
      }
    },
    onPageChange() {
      this.providerDetail = null;
      this.expandedUsageId = null;
      this.integrationDetail = null;

      // Auto-refresh overview every 30s while the user sits on it.
      // Cleared when they navigate elsewhere so we don't poll forever.
      if (this.overviewRefreshInterval) {
        clearInterval(this.overviewRefreshInterval);
        this.overviewRefreshInterval = null;
      }
      if (this.page === 'overview') {
        this.fetchOverview();
        this.overviewRefreshInterval = setInterval(() => this.fetchOverview(), 30000);
      }

      this.$nextTick(() => {
        if (this.page === 'usage') this.renderUsageChart();
        if (this.page === 'integrations') this.fetchIntegrations();
      });
    },
    async fetchStats() { try { const r = await fetch('/api/stats'); if (r.ok) this.stats = await r.json(); } catch (e) {} },

    // Single bundled fetch used to render the Overview homepage. We keep
    // this separate from the per-tab fetches above so the dashboard can
    // poll just this one endpoint while the user sits on the overview.
    async fetchOverview() {
      try {
        const r = await fetch('/api/overview');
        if (r.ok) this.overview = await r.json();
      } catch (e) {}
    },

    // Friendly time-of-day greeting used as the Overview hero header.
    // Falls back to "Hello" when the clock check is somehow off.
    overviewGreeting() {
      const h = new Date().getHours();
      if (h >= 5 && h < 12) return 'Good morning';
      if (h >= 12 && h < 17) return 'Good afternoon';
      if (h >= 17 && h < 22) return 'Good evening';
      return 'Hi there';
    },

    // Look up the registered icon name for a provider id. Driven by
    // the same /api/overview payload that powers the homepage cards,
    // so a new provider added on the backend automatically gets the
    // right Material Symbols icon everywhere it's rendered. Falls back
    // to "cloud" so unregistered providers still render something
    // sensible.
    providerIcon(name) {
      const providers = (this.overview && this.overview.providers) || [];
      const match = providers.find(p => p.name === name);
      return (match && match.icon) || 'cloud';
    },
    providerLabel(name) {
      const providers = (this.overview && this.overview.providers) || [];
      const match = providers.find(p => p.name === name);
      return (match && match.label) || name;
    },
    async fetchAccounts() { try { const r = await fetch('/api/accounts'); if (r.ok) { const d = await r.json(); this.accounts = d || []; } } catch (e) {} },
    async fetchKeys() {
      try {
        const r = await fetch('/api/keys');
        if (r.ok) { const d = await r.json(); this.keys = d || []; }
      } catch (e) {}
    },
    async fetchProviders() { try { const r = await fetch('/api/providers/stats'); if (r.ok) { const d = await r.json(); this.providerStats = d || []; } } catch (e) {} },
    async fetchUsageStats() {
      try {
        const r = await fetch('/api/usage/stats?period=' + encodeURIComponent(this.usagePeriod));
        if (r.ok) this.usageStats = await r.json();
      } catch (e) {}
    },
    async fetchRecentUsage() { try { const r = await fetch('/api/usage/recent'); if (r.ok) { const d = await r.json(); this.recentUsage = d || []; } } catch (e) {} },
    async fetchChart() {
      try {
        const r = await fetch('/api/usage/chart?period=' + encodeURIComponent(this.usagePeriod));
        if (r.ok) { const d = await r.json(); this.chartData = d || []; }
      } catch (e) {}
    },

    // Switch the usage period filter. Persists to localStorage and refreshes
    // both the stat cards and the time-series chart so they stay aligned.
    async setUsagePeriod(id) {
      if (this.usagePeriod === id) return;
      this.usagePeriod = id;
      try { localStorage.setItem('liam_usage_period', id); } catch (_) {}
      await Promise.all([this.fetchUsageStats(), this.fetchChart()]);
      this.$nextTick(() => this.renderUsageChart());
    },

    // Human label for the current period — shown next to the page title
    // and the chart header so it's always obvious which window is in view.
    usagePeriodLabel() {
      const opt = (this.usagePeriods || []).find(p => p.id === this.usagePeriod);
      return opt ? opt.label : 'Today';
    },
    async fetchRegistryModels() { try { const r = await fetch('/api/models'); if (r.ok) { const d = await r.json(); this.registryModels = d || []; } } catch (e) {} },
    async fetchBaseURL() {
      try {
        const r = await fetch('/api/settings/base-url');
        if (r.ok) {
          const d = await r.json();
          this.baseURL = d.base_url;
          this.defaultBaseURL = d.default_url;
        }
      } catch (e) {}
    },

    async fetchSyncStatus() {
      try {
        const r = await fetch('/api/sync/status');
        if (r.ok) this.syncStatus = await r.json();
      } catch (e) {}
    },

    async triggerSync() {
      try {
        const r = await fetch('/api/sync/now', { method: 'POST' });
        const d = await r.json();
        if (r.ok) {
          this.syncStatus.last_sync = d.last_sync;
          this.syncStatus.connected = true;
          await this.fetchSyncStatus();
          await this.fetchAccounts();
          await this.fetchKeys();
        } else {
          this.toast(d.error?.message || 'Sync failed', {kind:'error'});
        }
      } catch (e) { this.toast('Connection error', {kind:'error'}); }
    },

    // Combos
    async fetchCombos() {
      try { const r = await fetch('/api/combos'); if (r.ok) this.combos = await r.json() || []; } catch (e) {}
    },
    openCreateCombo() {
      this.editingCombo = null;
      this.comboForm = { name: '', models: [], strategy: 'fallback', sticky_limit: 1 };
      this.showComboModal = true;
    },
    openEditCombo(combo) {
      this.editingCombo = combo;
      this.comboForm = { name: combo.name, models: [...combo.models], strategy: combo.strategy, sticky_limit: combo.sticky_limit };
      this.showComboModal = true;
    },
    closeComboModal() { this.showComboModal = false; this.editingCombo = null; },
    addModelToCombo() {
      this.modelSelectTarget = { context: 'combo' };
      this.modelSelectCurrent = '';
      this.modelSelectSearch = '';
      this.showModelSelectModal = true;
    },
    removeModelFromCombo(index) { this.comboForm.models.splice(index, 1); },
    moveComboModel(index, dir) {
      const newIdx = index + dir;
      if (newIdx < 0 || newIdx >= this.comboForm.models.length) return;
      const temp = this.comboForm.models[index];
      this.comboForm.models[index] = this.comboForm.models[newIdx];
      this.comboForm.models[newIdx] = temp;
    },
    async saveCombo() {
      if (!this.comboForm.name || this.comboForm.models.length === 0) { this.toast('Name and at least 1 model required', {kind:'warn'}); return; }
      try {
        let r;
        if (this.editingCombo) {
          r = await fetch('/api/combos/' + this.editingCombo.id, { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(this.comboForm) });
        } else {
          r = await fetch('/api/combos', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(this.comboForm) });
        }
        if (!r.ok) { const d = await r.json(); this.toast(d.error?.message || 'Failed', {kind:'error'}); return; }
        await this.fetchCombos();
        this.closeComboModal();
      } catch (e) { this.toast('Error: ' + e.message, {kind:'error'}); }
    },
    async deleteCombo(id) {
      if (!await this.confirmDialog('Delete this combo? This cannot be undone.', {title:'Delete combo', danger:true, confirmLabel:'Delete'})) return;
      try { await fetch('/api/combos/' + id, { method: 'DELETE' }); await this.fetchCombos(); } catch (e) {}
    },

    // Routing
    async fetchRouting() {
      try { const r = await fetch('/api/settings/routing'); if (r.ok) this.routing = await r.json(); } catch (e) {}
    },
    async saveRouting() {
      this.routingMsg = '';
      try {
        const r = await fetch('/api/settings/routing', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({
          strategy: this.routing.strategy,
          sticky_limit: parseInt(this.routing.sticky_limit) || 3,
          provider_overrides: this.routing.provider_overrides || {}
        })});
        if (r.ok) { this.routingMsg = 'Saved'; setTimeout(() => this.routingMsg = '', 3000); }
      } catch (e) { this.routingMsg = 'Error'; }
    },
    setProviderRouting(provider, strategy, sticky) {
      if (!this.routing.provider_overrides) this.routing.provider_overrides = {};
      this.routing.provider_overrides[provider] = { strategy, sticky_limit: parseInt(sticky) || 3 };
    },

    // Drag-and-drop account reorder
    dragStart(e, accountId) { this.dragAccountId = accountId; e.dataTransfer.effectAllowed = 'move'; },
    dragOver(e) { e.preventDefault(); e.dataTransfer.dropEffect = 'move'; },
    async dropAccount(e, targetId) {
      e.preventDefault();
      if (!this.dragAccountId || this.dragAccountId === targetId) return;
      const ids = this.providerAccounts.map(a => a.id);
      const fromIdx = ids.indexOf(this.dragAccountId);
      const toIdx = ids.indexOf(targetId);
      if (fromIdx === -1 || toIdx === -1) return;
      ids.splice(fromIdx, 1);
      ids.splice(toIdx, 0, this.dragAccountId);
      this.dragAccountId = null;
      try {
        await fetch('/api/accounts/reorder', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({ ids }) });
        if (this.providerDetail) await this.openProvider(this.providerDetail);
      } catch (e) {}
    },

    // Add Account
    async openAddAccount(replaceAccountId) {
      this.showAddAccountModal = true;
      this.addAccountTab = 'oauth';
      this.addAccountForm = { refresh_token: '', email: '', callback_url: '' };
      this.addAccountOAuthURL = '';
      this.addAccountMsg = '';
      this.addAccountLoading = false;
      // When triggered by the "Re-import" banner, pass the existing
      // account ID so the backend updates that row in place instead
      // of creating a duplicate keyed on a different email.
      this.addAccountReplaceId = (typeof replaceAccountId === 'string' && replaceAccountId) ? replaceAccountId : '';

      // If AG provider, fetch OAuth URL
      if (this.providerDetail === 'antigravity') {
        try {
          const r = await fetch('/api/oauth/ag/authorize');
          if (r.ok) {
            const d = await r.json();
            this.addAccountOAuthURL = d.auth_url;
          }
        } catch (e) {}
      } else {
        // Kiro: default to token tab
        this.addAccountTab = 'token';
      }
    },
    closeAddAccount() {
      this.showAddAccountModal = false;
      this.addAccountMsg = '';
      this.addAccountReplaceId = '';
    },

    async submitAddAccountOAuth() {
      if (!this.addAccountForm.callback_url) { this.addAccountMsg = 'Paste the callback URL'; this.addAccountOk = false; return; }
      this.addAccountLoading = true;
      this.addAccountMsg = '';
      try {
        const r = await fetch('/api/oauth/ag/exchange', {
          method: 'POST',
          headers: {'Content-Type':'application/json'},
          body: JSON.stringify({ callback_url: this.addAccountForm.callback_url })
        });
        const d = await r.json();
        if (r.ok && d.success) {
          this.addAccountMsg = 'Account added: ' + d.email;
          this.addAccountOk = true;
          await this.fetchAccounts();
          await this.fetchProviders();
          if (this.providerDetail) await this.openProvider(this.providerDetail);
          setTimeout(() => this.closeAddAccount(), 2000);
        } else {
          this.addAccountMsg = d.error?.message || d.error || 'Failed';
          this.addAccountOk = false;
        }
      } catch (e) { this.addAccountMsg = 'Connection error'; this.addAccountOk = false; }
      this.addAccountLoading = false;
    },

    async submitAddAccountToken() {
      if (!this.addAccountForm.refresh_token) { this.addAccountMsg = 'Refresh token required'; this.addAccountOk = false; return; }
      this.addAccountLoading = true;
      this.addAccountMsg = '';

      const provider = this.providerDetail === 'antigravity' ? 'ag' : 'kiro';
      const endpoint = '/api/accounts/import/' + provider;
      const body = provider === 'kiro'
        ? { refresh_token: this.addAccountForm.refresh_token }
        : { refresh_token: this.addAccountForm.refresh_token, email: this.addAccountForm.email || '' };

      // When this modal was opened via the "Re-import" banner, hand the
      // existing account's ID to the backend so it updates that row in
      // place instead of inserting a duplicate.
      if (this.addAccountReplaceId) {
        body.account_id = this.addAccountReplaceId;
      }

      try {
        const r = await fetch(endpoint, {
          method: 'POST',
          headers: {'Content-Type':'application/json'},
          body: JSON.stringify(body)
        });
        const d = await r.json();
        if (r.ok && d.success) {
          this.addAccountMsg = (d.replaced ? 'Account re-imported: ' : 'Account added: ') + d.email;
          this.addAccountOk = true;
          await this.fetchAccounts();
          await this.fetchProviders();
          if (this.providerDetail) await this.openProvider(this.providerDetail);
          setTimeout(() => this.closeAddAccount(), 2000);
        } else {
          this.addAccountMsg = d.error?.message || d.error || 'Failed';
          this.addAccountOk = false;
        }
      } catch (e) { this.addAccountMsg = 'Connection error'; this.addAccountOk = false; }
      this.addAccountLoading = false;
    },

    // Edit Account
    openEditAccount(account) {
      this.editAccountId = account.id;
      this.editAccountEmail = account.email;
      this.editAccountMsg = '';
      this.showEditAccountModal = true;
    },
    closeEditAccount() { this.showEditAccountModal = false; },
    async submitEditAccount() {
      if (!this.editAccountEmail.trim()) { this.editAccountMsg = 'Name required'; return; }
      try {
        const r = await fetch('/api/accounts/' + this.editAccountId, {
          method: 'PATCH',
          headers: {'Content-Type':'application/json'},
          body: JSON.stringify({ email: this.editAccountEmail.trim() })
        });
        const d = await r.json();
        if (r.ok) {
          await this.fetchAccounts();
          if (this.providerDetail) await this.openProvider(this.providerDetail);
          this.closeEditAccount();
        } else {
          this.editAccountMsg = d.error?.message || 'Failed';
        }
      } catch (e) { this.editAccountMsg = 'Connection error'; }
    },

    // Account actions
    async refreshAccountQuota(id) {
      try {
        await fetch('/api/accounts/' + id + '/refresh-quota', { method: 'POST' });
        await this.fetchAccounts();
        if (this.providerDetail) await this.openProvider(this.providerDetail);
      } catch (e) {}
    },
    async refreshAllQuotas() {
      for (const a of this.providerAccounts) {
        await this.refreshAccountQuota(a.id);
      }
    },

    // Test a single account's credentials. Adopted from 9router pattern:
    // backend handles expiry-check + refresh + persist; UI just shows status.
    async testAccount(id) {
      if (!id || this.testingAccountId === id) return;
      this.testingAccountId = id;
      try {
        const r = await fetch('/api/accounts/' + id + '/test', { method: 'POST' });
        const d = await r.json();
        this.accountTestResults = {
          ...this.accountTestResults,
          [id]: {
            valid: !!d.valid,
            error: d.error || null,
            latency_ms: d.latency_ms || 0,
            refreshed: !!d.refreshed,
            ts: Date.now(),
          }
        };
        // Refresh accounts so token_expires_at / has_credentials reflect any
        // refresh we just performed server-side.
        if (d.refreshed) {
          await this.fetchAccounts();
          if (this.providerDetail) await this.openProvider(this.providerDetail);
        }
      } catch (e) {
        this.accountTestResults = {
          ...this.accountTestResults,
          [id]: { valid: false, error: 'Connection error', latency_ms: 0, refreshed: false, ts: Date.now() }
        };
      } finally {
        this.testingAccountId = null;
      }
    },

    accountTestStatusLabel(id) {
      const r = this.accountTestResults[id];
      if (!r) return 'Test connection';
      if (r.valid) {
        const note = r.refreshed ? ' (refreshed)' : '';
        return `OK ${r.latency_ms}ms${note}`;
      }
      return r.error || 'Test failed';
    },
    async deleteAccountById(id) {
      if (!await this.confirmDialog('Delete this account? This cannot be undone.', {title:'Delete account', danger:true, confirmLabel:'Delete'})) return;
      try {
        await fetch('/api/accounts/' + id, { method: 'DELETE' });
        await this.fetchAccounts();
        await this.fetchProviders();
        if (this.providerDetail) await this.openProvider(this.providerDetail);
      } catch (e) {}
    },
    async toggleAccountStatus(account) {
      const newStatus = account.status === 'active' ? 'disabled' : 'active';
      try {
        await fetch('/api/accounts', {
          method: 'POST',
          headers: {'Content-Type':'application/json'},
          body: JSON.stringify({ ...account, status: newStatus, credentials: undefined })
        });
        await this.fetchAccounts();
        if (this.providerDetail) await this.openProvider(this.providerDetail);
      } catch (e) {}
    },

    // Auto-refresh toggle. Persists the preference in localStorage so the
    // checkbox state survives reload/restart.
    toggleAccountAutoRefresh() {
      localStorage.setItem('liam_account_auto_refresh', this.accountAutoRefresh ? '1' : '0');
      // Always clear any existing interval first so toggling off->on doesn't
      // double-stack timers.
      if (this.accountRefreshInterval) clearInterval(this.accountRefreshInterval);
      if (this.accountCountdownInterval) clearInterval(this.accountCountdownInterval);
      this.accountRefreshInterval = null;
      this.accountCountdownInterval = null;

      if (this.accountAutoRefresh) {
        this.accountRefreshCountdown = 60;
        this.accountRefreshInterval = setInterval(() => {
          this.refreshAllQuotas();
          this.accountRefreshCountdown = 60;
        }, 60000);
        this.accountCountdownInterval = setInterval(() => {
          if (this.accountRefreshCountdown > 0) this.accountRefreshCountdown--;
        }, 1000);
      }
    },

    buildOverviewStats() {
      this.overviewStats = [
        { label: 'Status', value: 'Online', color: 'text-emerald-400' },
        { label: 'Accounts', value: this.stats.accounts_total || 0, color: 'text-brand-light' },
        { label: 'Active', value: this.stats.accounts_active || 0, color: 'text-emerald-400' },
        { label: 'Requests', value: this.usageStats.total_requests || 0, color: 'text-zinc-100' },
        { label: 'API Keys', value: this.stats.api_keys_total || 0, color: 'text-zinc-100' },
      ];
    },

    // Provider detail
    providerNameToAlias(name) {
      if (name === 'antigravity') return 'ag';
      if (name === 'kiro') return 'kr';
      return name;
    },
    async openProvider(name) {
      this.providerDetail = name;
      this.providerAccounts = this.accounts.filter(a => a.provider === name);
      // Mirror the selection into the URL so refresh restores it.
      this.setHash('providers', name);
      const alias = this.providerNameToAlias(name);
      try {
        const r = await fetch('/api/models?provider=' + alias);
        if (r.ok) this.providerModels = await r.json() || [];
      } catch (e) { this.providerModels = []; }
      // Activate auto-refresh if it was previously enabled. We only spawn
      // the interval the first time the provider page is opened (or after
      // navigating away + back) — toggleAccountAutoRefresh handles the
      // de-dup.
      if (this.accountAutoRefresh && !this.accountRefreshInterval) {
        this.toggleAccountAutoRefresh();
      }
    },
    closeProvider() {
      this.providerDetail = null;
      this.providerModels = [];
      // Drop the detail segment from the URL so a refresh stays on the
      // providers grid instead of reopening the closed detail page.
      this.setHash('providers');
      // Stop auto-refresh interval when leaving the provider detail page so
      // we don't keep ticking for nothing in the background.
      if (this.accountRefreshInterval) clearInterval(this.accountRefreshInterval);
      if (this.accountCountdownInterval) clearInterval(this.accountCountdownInterval);
      this.accountRefreshInterval = null;
      this.accountCountdownInterval = null;
    },

    // Model test
    async testModel(modelId) {
      this.testingModel = modelId;
      this.testResults[modelId] = null;
      try {
        const r = await fetch('/api/models/test', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({model: modelId})
        });
        this.testResults[modelId] = await r.json();
      } catch (e) {
        this.testResults[modelId] = {ok: false, error: 'Connection error'};
      }
      this.testingModel = null;
      setTimeout(() => { delete this.testResults[modelId]; }, 10000);
    },

    // Add Model modal
    openAddModel(providerAlias) {
      this.newModel = { provider_alias: providerAlias || 'ag', model_id: '', display_name: '', type: 'llm' };
      this.newModelTestResult = null;
      this.showAddModelModal = true;
    },
    closeAddModel() { this.showAddModelModal = false; this.newModelTestResult = null; },
    async testNewModel() {
      if (!this.newModel.model_id) return;
      this.newModelTesting = true;
      this.newModelTestResult = null;
      const fullId = this.newModel.provider_alias + '/' + this.newModel.model_id;
      try {
        const r = await fetch('/api/models/test', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({model: fullId})
        });
        this.newModelTestResult = await r.json();
      } catch (e) {
        this.newModelTestResult = {ok: false, error: 'Connection error'};
      }
      this.newModelTesting = false;
    },
    async submitAddModel() {
      if (!this.newModel.model_id) { this.toast('Model ID required', {kind:'warn'}); return; }
      try {
        const r = await fetch('/api/models/custom', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(this.newModel)
        });
        const d = await r.json();
        if (!r.ok) { this.toast(d.error?.message || 'Failed', {kind:'error'}); return; }
        await this.fetchRegistryModels();
        if (this.providerDetail) await this.openProvider(this.providerDetail);
        this.closeAddModel();
      } catch (e) { this.toast('Error: ' + e.message, {kind:'error'}); }
    },
    async removeModel(modelId) {
      if (!await this.confirmDialog('Remove ' + modelId + ' from the registry?', {title:'Remove model', danger:true, confirmLabel:'Remove'})) return;
      try {
        await fetch('/api/models/custom/' + modelId, { method: 'DELETE' });
        await this.fetchRegistryModels();
        if (this.providerDetail) await this.openProvider(this.providerDetail);
      } catch (e) {}
    },
    async refreshProviderModels() {
      if (!this.providerDetail) return;
      const alias = this.providerNameToAlias(this.providerDetail);
      try {
        const r = await fetch('/api/providers/' + alias + '/refresh-models', { method: 'POST' });
        const d = await r.json();
        if (!r.ok) { this.toast(d.error?.message || 'Failed to fetch', {kind:'error'}); return; }
        if (d.new_models && d.new_models.length > 0) this.toast(d.new_models.length + ' new models found upstream', {kind:'success'});
        else this.toast('No new models found', {kind:'info'});
      } catch (e) { this.toast('Error: ' + e.message, {kind:'error'}); }
    },
    copyModelId(modelId) { navigator.clipboard.writeText(modelId); },

    // Keys
    openCreateKey() {
      this.newKeyName = '';
      this.createdKey = null;
      this.showCreateKeyModal = true;
    },
    closeCreateKey() {
      this.showCreateKeyModal = false;
      this.createdKey = null;
      this.newKeyName = '';
    },
    async submitCreateKey() {
      if (!this.newKeyName.trim()) return;
      try {
        const r = await fetch('/api/keys', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({name: this.newKeyName.trim()})
        });
        const d = await r.json();
        if (!r.ok) { this.toast(d.error?.message || 'Failed', {kind:'error'}); return; }
        this.createdKey = d.key;
        // Stash the raw key locally keyed by id so the Integrations page
        // can auto-fill the apply request without making the user paste
        // it manually. Backend never returns the raw key again — this is
        // the only chance to capture it.
        try {
          const cache = JSON.parse(localStorage.getItem('liam_raw_keys') || '{}');
          cache[d.id] = d.key;
          localStorage.setItem('liam_raw_keys', JSON.stringify(cache));
        } catch (_) { /* ignore quota / privacy mode */ }
        await this.fetchKeys();
      } catch (e) { this.toast('Error: ' + e.message, {kind:'error'}); }
    },

    // ---- Raw key cache helpers ----
    // Backend hashes API keys and only ever shows the raw value at create
    // time. We stash that raw value in localStorage so the Integrations
    // tab can apply it without the user pasting manually. Helpers below
    // hide the cache implementation from callers.
    getRawApiKey(keyId) {
      if (!keyId) return '';
      try {
        const cache = JSON.parse(localStorage.getItem('liam_raw_keys') || '{}');
        return cache[keyId] || '';
      } catch (_) { return ''; }
    },
    forgetRawApiKey(keyId) {
      try {
        const cache = JSON.parse(localStorage.getItem('liam_raw_keys') || '{}');
        if (cache[keyId]) {
          delete cache[keyId];
          localStorage.setItem('liam_raw_keys', JSON.stringify(cache));
        }
      } catch (_) { /* noop */ }
    },
    async deleteKey(id) {
      if (!await this.confirmDialog('Delete this API key? This cannot be undone.', {title:'Delete API key', danger:true, confirmLabel:'Delete'})) return;
      try {
        const r = await fetch('/api/keys/' + id, { method: 'DELETE' });
        if (r.ok) {
          // Drop the cached raw key (if any) so the dropdown doesn't
          // keep handing it to integrations after the key is gone.
          this.forgetRawApiKey(id);
          await this.fetchKeys();
        }
      } catch (e) {}
    },
    toggleKeyVisibility(id) {
      this.keyVisibility[id] = !this.keyVisibility[id];
    },
    copyKeyPrefix(prefix) {
      navigator.clipboard.writeText(prefix);
    },

    // Usage detail (drawer-based, mirrors the 9router request details UI)
    async openUsageDetail(id) {
      if (!id) return;
      this.expandedUsageId = id;
      this.expandedUsageData = null;
      this.usageDetailOpen = true;
      // Reset section open-state per row so the user always sees
      // request + response collapsibles in their default state.
      this.usageSections = { request: true, response: true };
      try {
        const r = await fetch('/api/usage/' + id);
        if (r.ok) this.expandedUsageData = await r.json();
      } catch (e) {}
    },
    closeUsageDetail() {
      this.usageDetailOpen = false;
      this.expandedUsageId = null;
      this.expandedUsageData = null;
    },
    toggleSection(name) {
      if (!this.usageSections) this.usageSections = {};
      this.usageSections[name] = !this.usageSections[name];
    },

    // Backward-compat shim — existing keyboard shortcuts still call
    // toggleUsageDetail; route them to the drawer flow.
    toggleUsageDetail(id) { this.openUsageDetail(id); },

    prettyJSON(str) {
      if (!str) return '';
      try { return JSON.stringify(JSON.parse(str), null, 2); } catch (e) { return str; }
    },

    // Extract assistant content from a usage log's response_body. The body
    // is either a JSON object (non-streaming) or an SSE stream of
    // chat.completion.chunk lines. We accumulate every delta.content +
    // delta.text so the drawer shows the actual reply text instead of raw
    // SSE noise.
    extractedContent(d) {
      if (!d || !d.response_body) return '';
      const body = d.response_body;
      // Try JSON first.
      try {
        const obj = JSON.parse(body);
        if (Array.isArray(obj?.choices) && obj.choices.length) {
          const c = obj.choices[0];
          if (c.message?.content) return c.message.content;
          if (typeof c.text === 'string') return c.text;
        }
      } catch (_) {}

      if (!body.includes('data:')) return '';
      let out = '';
      for (const line of body.split('\n')) {
        const trimmed = line.trim();
        if (!trimmed.startsWith('data:')) continue;
        const payload = trimmed.slice(5).trim();
        if (!payload || payload === '[DONE]') continue;
        try {
          const obj = JSON.parse(payload);
          for (const c of obj.choices || []) {
            const delta = c.delta || {};
            if (typeof delta.content === 'string') out += delta.content;
            else if (typeof delta.text === 'string') out += delta.text;
          }
        } catch (_) { /* ignore non-JSON SSE noise */ }
      }
      return out;
    },

    // Same idea for thinking/reasoning content. Models that emit
    // delta.reasoning_content stream their thoughts before the answer.
    extractedThinking(d) {
      if (!d || !d.response_body) return '';
      const body = d.response_body;
      if (!body.includes('data:')) {
        // Non-streaming: thinking field on message
        try {
          const obj = JSON.parse(body);
          const r = obj?.choices?.[0]?.message?.reasoning_content;
          return typeof r === 'string' ? r : '';
        } catch (_) { return ''; }
      }
      let out = '';
      for (const line of body.split('\n')) {
        const trimmed = line.trim();
        if (!trimmed.startsWith('data:')) continue;
        const payload = trimmed.slice(5).trim();
        if (!payload || payload === '[DONE]') continue;
        try {
          const obj = JSON.parse(payload);
          for (const c of obj.choices || []) {
            const delta = c.delta || {};
            if (typeof delta.reasoning_content === 'string') out += delta.reasoning_content;
          }
        } catch (_) {}
      }
      return out;
    },

    // SSE
    startSSE() {
      if (this.sse) this.sse.close();
      this.sse = new EventSource('/sse/requests');
      this.sse.onmessage = (e) => {
        try {
          const req = JSON.parse(e.data);

          // Two payload shapes flow through /sse/requests today:
          //   1) lightweight pre-log event (no id) — used by the live
          //      ticker on the Overview page.
          //   2) canonical post-log event (with id + created_at) — used
          //      to push real-time rows into the Recent Requests table
          //      without waiting for a refresh.
          if (req && req.id) {
            // Dedup by id; if it's already there (rare race), update
            // in place so latency/status reflect the latest.
            const idx = this.recentUsage.findIndex(u => u.id === req.id);
            const merged = {
              tokens_in: 0,
              tokens_out: 0,
              ...req,
            };
            if (idx === -1) {
              this.recentUsage.unshift(merged);
              if (this.recentUsage.length > 50) this.recentUsage.pop();
              // Keep the user looking at page 0 (newest) when they
              // haven't scrolled. If they're on a later page, leave
              // the cursor where it is.
              if (this.usagePage === 0) this.expandedUsageId = this.expandedUsageId;
            } else {
              this.recentUsage.splice(idx, 1, merged);
            }
          } else {
            // Lightweight feed — keep liveRequests for chart ticker.
            this.liveRequests.unshift(req);
            if (this.liveRequests.length > 50) this.liveRequests.pop();
          }
        } catch (err) {}
      };
    },

    // Charts
    renderChart() {
      const canvas = document.getElementById('chartCanvas');
      if (!canvas || !this.chartData || !this.chartData.length) return;
      if (this.chart) this.chart.destroy();
      this.chart = new Chart(canvas, {
        type: 'line',
        data: {
          labels: this.chartData.map(b => b.time),
          datasets: [{ label: 'Requests', data: this.chartData.map(b => b.requests), borderColor: '#922b21', backgroundColor: 'rgba(146,43,33,0.08)', borderWidth: 2, fill: true, tension: 0.4, pointRadius: 0 }]
        },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { display: false } }, scales: { x: { grid: { color: 'rgba(63,63,70,0.3)' }, ticks: { color: '#71717a', font: { size: 10 } } }, y: { grid: { color: 'rgba(63,63,70,0.3)' }, ticks: { color: '#71717a', font: { size: 10 } }, beginAtZero: true } } }
      });
    },
    renderUsageChart() {
      const canvas = document.getElementById('usageChartCanvas');
      if (!canvas) return;
      if (this.usageChart) {
        this.usageChart.destroy();
        this.usageChart = null;
      }
      // Treat empty data as a valid render — Chart.js still draws axes,
      // which is way better UX than a blank card. Previously we returned
      // early when chartData was empty and the canvas just disappeared.
      const buckets = Array.isArray(this.chartData) ? this.chartData : [];
      const labels = buckets.map(b => b.time);
      const reqs = buckets.map(b => b.requests || 0);
      const inK = buckets.map(b => Math.round(((b.in_tokens || 0)) / 1000));
      const outK = buckets.map(b => Math.round(((b.out_tokens || 0)) / 1000));
      this.usageChart = new Chart(canvas, {
        type: 'line',
        data: {
          labels,
          datasets: [
            { label: 'Requests', data: reqs, yAxisID: 'y',
              borderColor: '#922b21', backgroundColor: 'rgba(146,43,33,0.10)',
              borderWidth: 2, fill: true, tension: 0.4, pointRadius: 0 },
            { label: 'Input tokens (K)', data: inK, yAxisID: 'y1',
              borderColor: '#3b82f6', backgroundColor: 'rgba(59,130,246,0.05)',
              borderWidth: 1.5, fill: false, tension: 0.4, pointRadius: 0 },
            { label: 'Output tokens (K)', data: outK, yAxisID: 'y1',
              borderColor: '#27ae60', backgroundColor: 'rgba(39,174,96,0.05)',
              borderWidth: 1.5, fill: false, tension: 0.4, pointRadius: 0 },
          ],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          interaction: { mode: 'index', intersect: false },
          plugins: {
            legend: { labels: { color: '#a1a1aa', font: { size: 10 } } },
            tooltip: { intersect: false, mode: 'index' },
          },
          scales: {
            x: {
              grid: { color: 'rgba(63,63,70,0.3)' },
              ticks: { color: '#71717a', font: { size: 10 }, maxRotation: 0, autoSkipPadding: 12 },
            },
            y: {
              type: 'linear',
              position: 'left',
              grid: { color: 'rgba(63,63,70,0.3)' },
              ticks: { color: '#71717a', font: { size: 10 } },
              beginAtZero: true,
              title: { display: true, text: 'Requests', color: '#a1a1aa', font: { size: 10 } },
            },
            y1: {
              type: 'linear',
              position: 'right',
              grid: { drawOnChartArea: false },
              ticks: { color: '#71717a', font: { size: 10 } },
              beginAtZero: true,
              title: { display: true, text: 'Tokens (K)', color: '#a1a1aa', font: { size: 10 } },
            },
          },
        },
      });
    },

    copyEndpoint() { navigator.clipboard.writeText(this.endpoint); this.copied = true; setTimeout(() => this.copied = false, 2000); },
    copyText(text) { navigator.clipboard.writeText(text); },

    // Integrations
    async fetchIntegrations() {
      try {
        const r = await fetch('/api/integrations');
        if (r.ok) this.integrations = await r.json() || [];
      } catch (e) {}
    },
    async openIntegration(toolName) {
      this.integrationDetail = toolName;
      this.integrationDetailData = null;
      this.integrationApplyMsg = '';
      // Mirror selection into URL hash so refresh keeps the same tool open.
      this.setHash('integrations', toolName);

      // Initialize config. Pre-fill the API key from localStorage when
      // we still have the raw value from creation; otherwise leave the
      // dropdown unselected so the user knows they need to reveal one.
      const firstKey = this.keys.length > 0 ? this.keys[0] : null;
      const cachedRaw = firstKey ? this.getRawApiKey(firstKey.id) : '';
      this.integrationConfig = {
        base_url: this.baseURL || ('http://localhost:' + location.port + '/v1'),
        api_key_id: firstKey ? firstKey.id : '',
        api_key: cachedRaw, // raw key when available, empty otherwise
        api_key_custom: false,
        models: {},
        agent_models: {},
        use_custom_url: false,
        custom_url: '',
      };

      try {
        const r = await fetch('/api/integrations/' + toolName);
        if (r.ok) {
          this.integrationDetailData = await r.json();
          // Initialize model slots with defaults
          if (this.integrationDetailData.model_slots) {
            for (const slot of this.integrationDetailData.model_slots) {
              this.integrationConfig.models[slot.key] = slot.default || '';
            }
          }
          await this.refreshSnippet();
        }
      } catch (e) {}
    },
    closeIntegration() {
      this.integrationDetail = null;
      this.integrationDetailData = null;
      this.integrationSnippet = '';
      // Drop detail segment so refresh shows the integrations grid.
      this.setHash('integrations');
    },
    async refreshSnippet() {
      if (!this.integrationDetail) return;
      try {
        const apiKey = this.getActualApiKey();
        const baseURL = this.integrationConfig.use_custom_url
          ? this.integrationConfig.custom_url
          : this.integrationConfig.base_url;
        const r = await fetch('/api/integrations/' + this.integrationDetail + '/snippet', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({
            api_key: apiKey,
            base_url: baseURL,
            models: this.integrationConfig.models,
            agent_models: this.integrationConfig.agent_models,
          })
        });
        if (r.ok) {
          const d = await r.json();
          this.integrationSnippet = d.snippet;
        }
      } catch (e) {}
    },
    getActualApiKey() {
      const k = this.integrationConfig.api_key || '';
      // Custom mode: use the value as-is (user pasted manually).
      if (this.integrationConfig.api_key_custom) {
        return k;
      }
      // Dropdown mode: prefer the cached raw key (saved at creation
      // time). Fall back to whatever is in the field — which may still
      // be a placeholder; the caller validates before sending.
      const fromCache = this.getRawApiKey(this.integrationConfig.api_key_id);
      return fromCache || k || '';
    },
    // Called when the user picks a different key from the dropdown.
    // Refreshes the cached raw value so the snippet preview shows the
    // real key (or warns when it's not available).
    onIntegrationKeyChange() {
      const id = this.integrationConfig.api_key_id;
      const raw = this.getRawApiKey(id);
      this.integrationConfig.api_key = raw;
      this.refreshSnippet();
    },
    async applyIntegration() {
      if (!this.integrationDetail) return;
      const apiKey = this.getActualApiKey();
      // Reject obvious placeholders. The full raw key always starts with
      // `lyd-` and is far longer than the prefix; if we can't find it we
      // tell the user how to recover instead of failing silently.
      if (!apiKey || apiKey === '<YOUR_KEY>' || apiKey.endsWith('...') || !apiKey.startsWith('lyd-')) {
        if (!this.integrationConfig.api_key_custom && this.integrationConfig.api_key_id) {
          this.toast("We don't have the raw value for this key on this device. Click 'Custom Key' and paste the full key, or create a new one in the Keys page (the raw value is shown once at creation).", {kind:'warn', title:'Raw key unavailable'});
        } else {
          this.toast('Please enter a real API key (or create one in the Keys page).', {kind:'warn'});
        }
        return;
      }
      const baseURL = this.integrationConfig.use_custom_url
        ? this.integrationConfig.custom_url
        : this.integrationConfig.base_url;
      try {
        const r = await fetch('/api/integrations/' + this.integrationDetail + '/apply', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({
            api_key: apiKey,
            base_url: baseURL,
            models: this.integrationConfig.models,
            agent_models: this.integrationConfig.agent_models,
          })
        });
        const d = await r.json();
        if (r.ok) {
          this.integrationApplyMsg = 'Applied successfully';
          this.integrationApplyOk = true;
          // Save preset if custom URL was used
          if (this.integrationConfig.use_custom_url && baseURL) {
            this.savePreset(baseURL);
          }
          // Refresh status
          const statusR = await fetch('/api/integrations/' + this.integrationDetail);
          if (statusR.ok) this.integrationDetailData = await statusR.json();
          await this.fetchIntegrations();
        } else {
          this.integrationApplyMsg = d.error?.message || 'Failed';
          this.integrationApplyOk = false;
        }
      } catch (e) {
        this.integrationApplyMsg = 'Connection error';
        this.integrationApplyOk = false;
      }
      setTimeout(() => { this.integrationApplyMsg = ''; }, 5000);
    },
    async resetIntegration() {
      if (!await this.confirmDialog('Remove LIAM config from ' + this.integrationDetail + '?', {title:'Reset integration', danger:true, confirmLabel:'Reset'})) return;
      try {
        const r = await fetch('/api/integrations/' + this.integrationDetail + '/reset', { method: 'POST' });
        if (r.ok) {
          this.integrationApplyMsg = 'Removed';
          this.integrationApplyOk = true;
          const statusR = await fetch('/api/integrations/' + this.integrationDetail);
          if (statusR.ok) this.integrationDetailData = await statusR.json();
          await this.fetchIntegrations();
        }
      } catch (e) {}
      setTimeout(() => { this.integrationApplyMsg = ''; }, 3000);
    },
    openManualConfig() {
      if (!this.integrationDetailData) return;
      this.manualConfigContent = this.integrationSnippet;
      this.manualConfigPath = this.integrationDetailData.config_path || '';
      this.manualConfigTool = this.integrationDetailData.display_name;
      this.showManualConfigModal = true;
    },

    // Model Select Modal
    openModelSelect(toolName, slotKey, currentValue) {
      this.modelSelectTarget = { tool: toolName, slot: slotKey };
      this.modelSelectCurrent = currentValue || '';
      this.modelSelectSearch = '';
      this.showModelSelectModal = true;
    },
    selectModel(modelId) {
      if (this.modelSelectTarget && this.modelSelectTarget.context === 'combo') {
        // Adding model to combo form
        if (!this.comboForm.models.includes(modelId)) {
          this.comboForm.models.push(modelId);
        }
      } else if (this.modelSelectTarget && this.modelSelectTarget.slot) {
        this.integrationConfig.models[this.modelSelectTarget.slot] = modelId;
        this.refreshSnippet();
      }
      this.showModelSelectModal = false;
    },

    // Save base URL (from integrations page custom URL flow)
    async saveBaseURL() {
      const url = this.integrationConfig.use_custom_url
        ? this.integrationConfig.custom_url
        : this.integrationConfig.base_url;
      try {
        await fetch('/api/settings/base-url', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({base_url: url})
        });
        this.baseURL = url;
      } catch (e) {}
    },

    // Save base URL from settings page
    async saveSettingsBaseURL() {
      this.baseURLMsg = '';
      try {
        const r = await fetch('/api/settings/base-url', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({base_url: this.baseURL || ''})
        });
        if (r.ok) {
          this.baseURLMsg = 'Saved';
          this.baseURLOk = true;
          await this.fetchBaseURL();
          if (this.baseURL && !this.savedPresets.includes(this.baseURL)) {
            this.savePreset(this.baseURL);
          }
        } else {
          const d = await r.json();
          this.baseURLMsg = d.error?.message || 'Failed';
          this.baseURLOk = false;
        }
      } catch (e) {
        this.baseURLMsg = 'Connection error';
        this.baseURLOk = false;
      }
      setTimeout(() => { this.baseURLMsg = ''; }, 4000);
    },

    async resetSettingsBaseURL() {
      this.baseURL = '';
      try {
        await fetch('/api/settings/base-url', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({base_url: ''})
        });
        await this.fetchBaseURL();
        this.baseURLMsg = 'Reset to default';
        this.baseURLOk = true;
      } catch (e) {
        this.baseURLMsg = 'Connection error';
        this.baseURLOk = false;
      }
      setTimeout(() => { this.baseURLMsg = ''; }, 4000);
    },

    // Harvest
    async startHarvest() {
      if (!this.harvest.accounts.trim()) { this.toast('Paste accounts first', {kind:'warn'}); return; }
      const r = await fetch('/api/harvest/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ provider: this.harvest.provider, accounts: this.harvest.accounts, concurrency: parseInt(this.harvest.concurrency), headless: this.harvest.headless === 'true' }) });
      const d = await r.json();
      if (!r.ok) { this.toast(d.error || 'Failed', {kind:'error'}); return; }
    },
    async stopHarvest() { await fetch('/api/harvest/stop', { method: 'POST' }); },
    startHarvestPoll() {
      if (this.harvestPoll) clearInterval(this.harvestPoll);
      // Initial fetch to know current state
      this.fetchHarvestStatus();
      // Smart polling: only run when needed
      this.harvestPoll = setInterval(() => {
        // Skip if not running AND not on harvest page
        if (!this.harvestStatus.running && this.page !== 'harvest') return;
        this.fetchHarvestStatus();
      }, 2000);
      // Independent 1-second wall-clock tick so the elapsed/ETA labels keep
      // animating between status polls. Unlike fetchHarvestStatus this does
      // not hit the server — it only updates `harvestNow`.
      if (this.harvestNowTimer) clearInterval(this.harvestNowTimer);
      this.harvestNowTimer = setInterval(() => {
        if (this.harvestStatus.running) {
          this.harvestNow = Math.floor(Date.now() / 1000);
        }
      }, 1000);
    },
    async fetchHarvestStatus() {
      try {
        const r = await fetch('/api/harvest/status');
        if (r.ok) {
          this.harvestStatus = await r.json();
          if (!this.harvestStatus.logs) this.harvestStatus.logs = [];
          if (!this.harvestStatus.accounts) this.harvestStatus.accounts = [];
          this.harvestNow = Math.floor(Date.now() / 1000);
          if (!this.harvestStatus.running && this.harvestStatus.success > 0) {
            this.fetchAccounts(); this.fetchStats(); this.fetchProviders(); this.buildOverviewStats();
          }
        }
      } catch (e) {}
    },

    // Builds a one-line digest of the latest run. Useful for posting status
    // updates into a chat or just reading at a glance after a long batch.
    async copyHarvestSummary() {
      const s = this.harvestStatus;
      const elapsed = this.formatDuration(this.harvestElapsed);
      const successRate = s.total > 0 ? Math.round((s.success / s.total) * 100) : 0;
      const summary = `Harvest ${s.provider || 'ag'}: ${s.success}/${s.total} success (${successRate}%), ${s.failed} failed, ${elapsed}`;
      try {
        await navigator.clipboard.writeText(summary);
        this.toast('Summary copied', { kind: 'success' });
      } catch (e) {
        this.toast('Copy failed: ' + e.message, { kind: 'error' });
      }
    },

    // Settings
    async changePassword() {
      this.settings.msg = '';
      const r = await fetch('/api/auth/password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ current: this.settings.currentPw, new: this.settings.newPw }) });
      const d = await r.json();
      if (r.ok) { this.settings.msg = 'Password changed. Logging out...'; this.settings.ok = true; setTimeout(() => this.logout(), 2000); }
      else { this.settings.msg = d.error || 'Failed'; this.settings.ok = false; }
    },

    // Helpers
    formatNum(n) { if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M'; if (n >= 1000) return (n / 1000).toFixed(1) + 'K'; return String(n); },

    // Compact token formatter for the recent-requests row. Returns '—'
    // for zero/missing values so the column visually reads "no data"
    // instead of an ambiguous "0".
    formatTokenCount(n) {
      const v = Number(n) || 0;
      if (v <= 0) return '—';
      return this.formatNum(v);
    },
    quotaPercent(a) { return a.quota_total > 0 ? Math.round((a.quota_remaining / a.quota_total) * 100) : 0; },
    quotaColor(a) { const p = this.quotaPercent(a); return p > 50 ? 'bg-ok' : p > 20 ? 'bg-warn' : 'bg-err'; },

    // True when the account is currently sitting out an upstream-error
    // cooldown (per-account backoff window). Drives the "cooldown 8s"
    // badge on each account card. Auto-clears once cooldown_until lapses.
    isInBackoff(a) {
      if (!a || !a.cooldown_until) return false;
      const t = Date.parse(a.cooldown_until);
      if (!isFinite(t)) return false;
      return t > Date.now();
    },

    // Short remaining-time string for the cooldown badge: "8s", "2m", "5m".
    cooldownRemaining(a) {
      if (!a || !a.cooldown_until) return '';
      const ms = Date.parse(a.cooldown_until) - Date.now();
      if (!isFinite(ms) || ms <= 0) return '0s';
      const sec = Math.ceil(ms / 1000);
      if (sec < 60) return sec + 's';
      const min = Math.ceil(sec / 60);
      if (min < 60) return min + 'm';
      const hr = Math.floor(min / 60);
      const rem = min % 60;
      return rem ? hr + 'h' + rem + 'm' : hr + 'h';
    },

    // True when the credentials JSON does not have a usable token yet
    // (server strips creds before sending; we rely on has_credentials).
    isTokenExpired(a) {
      if (!a || !a.token_expires_at) return false;
      const t = Date.parse(a.token_expires_at);
      if (!isFinite(t)) return false;
      return t <= Date.now();
    },

    tokenExpiryClass(a) {
      if (!a || !a.token_expires_at) return 'text-txt-3';
      const ms = Date.parse(a.token_expires_at) - Date.now();
      if (!isFinite(ms)) return 'text-txt-3';
      if (ms <= 0) return 'text-err';
      if (ms < 5 * 60 * 1000) return 'text-warn';
      return 'text-txt-3';
    },

    // "expired 3m ago" / "expires in 42m" / "expires 14:30"
    formatTokenExpiry(a) {
      if (!a || !a.token_expires_at) return '';
      const t = Date.parse(a.token_expires_at);
      if (!isFinite(t)) return 'invalid expiry';
      const ms = t - Date.now();
      const abs = Math.abs(ms);
      const mins = Math.round(abs / 60000);
      const past = ms <= 0;
      if (mins < 1) return past ? 'expired just now' : 'expires in <1m';
      if (mins < 60) return past ? `expired ${mins}m ago` : `expires in ${mins}m`;
      const hrs = Math.round(mins / 60);
      if (hrs < 48) return past ? `expired ${hrs}h ago` : `expires in ${hrs}h`;
      const days = Math.round(hrs / 24);
      return past ? `expired ${days}d ago` : `expires in ${days}d`;
    },

    // Format a number compactly: keep up to 1 decimal for fractional values,
    // otherwise return the integer. Used for per-resource breakdown labels.
    quotaFmt(v) {
      if (v === null || v === undefined) return '0';
      const n = Number(v);
      if (!isFinite(n)) return '0';
      if (Number.isInteger(n)) return String(n);
      return n.toFixed(1);
    },

    // Pretty label for a quota_breakdown key. The Kiro upstream returns
    // resource keys like "agentic_request" and "agentic_request_freetrial";
    // turn them into human-friendly headings.
    quotaResourceLabel(key) {
      if (!key) return 'usage';
      const lower = String(key).toLowerCase();
      const map = {
        agentic_request: 'agentic',
        agentic_request_freetrial: 'agentic (free trial)',
        chat: 'chat',
        chat_freetrial: 'chat (free trial)',
      };
      if (map[lower]) return map[lower];
      return lower.replace(/_/g, ' ');
    },

    hasQuotaBreakdown(a) {
      const b = a && a.quota_breakdown;
      if (!b) return false;
      try {
        const obj = typeof b === 'string' ? JSON.parse(b) : b;
        return obj && typeof obj === 'object' && Object.keys(obj).length > 0;
      } catch (e) {
        return false;
      }
    },

    quotaBreakdownRows(a) {
      if (!a) return [];
      let raw = a.quota_breakdown;
      if (!raw) return [];
      if (typeof raw === 'string') {
        try { raw = JSON.parse(raw); } catch (e) { return []; }
      }
      if (!raw || typeof raw !== 'object') return [];

      // Sort so the headline AGENTIC_REQUEST bucket comes first, then the
      // free-trial mirror, then everything else alphabetically.
      const order = (k) => {
        if (k === 'agentic_request') return 0;
        if (k === 'agentic_request_freetrial') return 1;
        if (k.endsWith('_freetrial')) return 3;
        return 2;
      };

      return Object.entries(raw)
        .map(([key, entry]) => {
          const used = Number(entry?.used) || 0;
          const total = Number(entry?.total) || 0;
          const remaining = Math.max(total - used, 0);
          const percent = total > 0 ? Math.round((remaining / total) * 100) : 0;
          return {
            key,
            label: this.quotaResourceLabel(key),
            used,
            total,
            remaining,
            percent,
            usedFmt: this.quotaFmt(used),
            totalFmt: total > 0 ? this.quotaFmt(total) : '?',
            resetAt: entry?.reset_at || '',
          };
        })
        .sort((x, y) => order(x.key) - order(y.key) || x.key.localeCompare(y.key));
    },

    // ---- Usage pagination helpers (client-side, 10 per page) ----
    pagedUsage() {
      const start = this.usagePage * this.usagePageSize;
      return this.recentUsage.slice(start, start + this.usagePageSize);
    },
    usagePageRangeLabel() {
      if (!this.recentUsage.length) return '';
      const start = this.usagePage * this.usagePageSize + 1;
      const end = Math.min((this.usagePage + 1) * this.usagePageSize, this.recentUsage.length);
      return start + '-' + end + ' of ' + this.recentUsage.length;
    },
    usagePagePrev() {
      if (this.usagePage > 0) this.usagePage--;
      this.expandedUsageId = null;
    },
    usagePageNext() {
      if ((this.usagePage + 1) * this.usagePageSize < this.recentUsage.length) this.usagePage++;
      this.expandedUsageId = null;
    },

    timeAgo(t) {
      if (!t) return '-';
      const ms = new Date(t).getTime();
      if (!isFinite(ms)) return '-';
      const diff = Date.now() - ms;
      const future = diff < 0;
      const abs = Math.abs(diff);
      const fmt = (n, unit) => future ? `in ${n}${unit}` : `${n}${unit} ago`;
      if (abs < 60000) return fmt(Math.round(abs/1000), 's');
      if (abs < 3600000) return fmt(Math.round(abs/60000), 'm');
      if (abs < 86400000) return fmt(Math.round(abs/3600000), 'h');
      return fmt(Math.round(abs/86400000), 'd');
    },

    // "15/05/2026 17:30" — fixed length so it doesn't shift the layout.
    // Used for the title attribute on relative-time labels.
    formatDateTime(t) {
      if (!t) return '';
      const d = new Date(t);
      if (!isFinite(d.getTime())) return '';
      const pad = (n) => String(n).padStart(2, '0');
      return `${pad(d.getDate())}/${pad(d.getMonth()+1)}/${d.getFullYear()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    },

    // Compact "15/05 17:30" for inline display when a relative label
    // is too noisy (we use it for the resets timestamp under each bar).
    formatShortDateTime(t) {
      if (!t) return '';
      const d = new Date(t);
      if (!isFinite(d.getTime())) return '';
      const pad = (n) => String(n).padStart(2, '0');
      return `${pad(d.getDate())}/${pad(d.getMonth()+1)} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
    },
  };
}
