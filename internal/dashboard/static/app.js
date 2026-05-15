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

    // Harvest
    harvest: { provider: 'ag', concurrency: '4', headless: 'false', accounts: '' },
    harvestStatus: { running: false, success: 0, failed: 0, total: 0, logs: [], accounts: [] },
    harvestPoll: null,

    // Settings
    settings: { currentPw: '', newPw: '', msg: '', ok: false },

    // UI
    copied: false,
    endpoint: '',
    chart: null,
    usageChart: null,
    sse: null,

    get harvestAccountCount() {
      if (!this.harvest.accounts) return 0;
      return this.harvest.accounts.split('\n').filter(l => l.trim() && l.includes(':')).length;
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
      this.loadPresets();
      if (this.token) {
        this.verifyToken();
      } else {
        this.authChecked = true;
      }
    },

    loadPresets() {
      try {
        const saved = localStorage.getItem('liam_endpoint_presets');
        if (saved) this.savedPresets = JSON.parse(saved);
      } catch (e) { this.savedPresets = []; }
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
        this.fetchAccounts(),
        this.fetchKeys(),
        this.fetchProviders(),
        this.fetchUsageStats(),
        this.fetchRecentUsage(),
        this.fetchChart(),
        this.fetchRegistryModels(),
        this.fetchBaseURL(),
        this.fetchSyncStatus(),
      ]);
      this.buildOverviewStats();
      this.startSSE();
      this.startHarvestPoll();
      this.$nextTick(() => this.renderChart());
    },
    onPageChange() {
      this.providerDetail = null;
      this.expandedUsageId = null;
      this.integrationDetail = null;
      this.$nextTick(() => {
        if (this.page === 'overview') this.renderChart();
        if (this.page === 'usage') this.renderUsageChart();
        if (this.page === 'integrations') this.fetchIntegrations();
      });
    },
    async fetchStats() { try { const r = await fetch('/api/stats'); if (r.ok) this.stats = await r.json(); } catch (e) {} },
    async fetchAccounts() { try { const r = await fetch('/api/accounts'); if (r.ok) { const d = await r.json(); this.accounts = d || []; } } catch (e) {} },
    async fetchKeys() {
      try {
        const r = await fetch('/api/keys');
        if (r.ok) { const d = await r.json(); this.keys = d || []; }
      } catch (e) {}
    },
    async fetchProviders() { try { const r = await fetch('/api/providers/stats'); if (r.ok) { const d = await r.json(); this.providerStats = d || []; } } catch (e) {} },
    async fetchUsageStats() { try { const r = await fetch('/api/usage/stats'); if (r.ok) this.usageStats = await r.json(); } catch (e) {} },
    async fetchRecentUsage() { try { const r = await fetch('/api/usage/recent'); if (r.ok) { const d = await r.json(); this.recentUsage = d || []; } } catch (e) {} },
    async fetchChart() { try { const r = await fetch('/api/usage/chart'); if (r.ok) { const d = await r.json(); this.chartData = d || []; } } catch (e) {} },
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
          alert(d.error?.message || 'Sync failed');
        }
      } catch (e) { alert('Connection error'); }
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
      const alias = this.providerNameToAlias(name);
      try {
        const r = await fetch('/api/models?provider=' + alias);
        if (r.ok) this.providerModels = await r.json() || [];
      } catch (e) { this.providerModels = []; }
    },
    closeProvider() { this.providerDetail = null; this.providerModels = []; },

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
      if (!this.newModel.model_id) { alert('Model ID required'); return; }
      try {
        const r = await fetch('/api/models/custom', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(this.newModel)
        });
        const d = await r.json();
        if (!r.ok) { alert(d.error?.message || 'Failed'); return; }
        await this.fetchRegistryModels();
        if (this.providerDetail) await this.openProvider(this.providerDetail);
        this.closeAddModel();
      } catch (e) { alert('Error: ' + e.message); }
    },
    async removeModel(modelId) {
      if (!confirm('Remove ' + modelId + '?')) return;
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
        if (!r.ok) { alert(d.error?.message || 'Failed to fetch'); return; }
        if (d.new_models && d.new_models.length > 0) alert(d.new_models.length + ' new models found upstream');
        else alert('No new models found');
      } catch (e) { alert('Error: ' + e.message); }
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
        if (!r.ok) { alert(d.error?.message || 'Failed'); return; }
        this.createdKey = d.key;
        await this.fetchKeys();
      } catch (e) { alert('Error: ' + e.message); }
    },
    async deleteKey(id) {
      if (!confirm('Delete this key? This cannot be undone.')) return;
      try {
        const r = await fetch('/api/keys/' + id, { method: 'DELETE' });
        if (r.ok) await this.fetchKeys();
      } catch (e) {}
    },
    toggleKeyVisibility(id) {
      this.keyVisibility[id] = !this.keyVisibility[id];
    },
    copyKeyPrefix(prefix) {
      navigator.clipboard.writeText(prefix);
    },

    // Usage detail
    async toggleUsageDetail(id) {
      if (this.expandedUsageId === id) {
        this.expandedUsageId = null;
        this.expandedUsageData = null;
        return;
      }
      this.expandedUsageId = id;
      this.expandedUsageData = null;
      try {
        const r = await fetch('/api/usage/' + id);
        if (r.ok) this.expandedUsageData = await r.json();
      } catch (e) {}
    },
    prettyJSON(str) {
      if (!str) return '';
      try { return JSON.stringify(JSON.parse(str), null, 2); } catch (e) { return str; }
    },

    // SSE
    startSSE() {
      if (this.sse) this.sse.close();
      this.sse = new EventSource('/sse/requests');
      this.sse.onmessage = (e) => {
        try {
          const req = JSON.parse(e.data);
          this.liveRequests.unshift(req);
          if (this.liveRequests.length > 50) this.liveRequests.pop();
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
      if (!canvas || !this.chartData || !this.chartData.length) return;
      if (this.usageChart) this.usageChart.destroy();
      this.usageChart = new Chart(canvas, {
        type: 'line',
        data: {
          labels: this.chartData.map(b => b.time),
          datasets: [
            { label: 'Requests', data: this.chartData.map(b => b.requests), borderColor: '#922b21', backgroundColor: 'rgba(146,43,33,0.08)', borderWidth: 2, fill: true, tension: 0.4, pointRadius: 0 },
            { label: 'Tokens (k)', data: this.chartData.map(b => Math.round(b.tokens / 1000)), borderColor: '#27ae60', backgroundColor: 'rgba(39,174,96,0.05)', borderWidth: 1.5, fill: false, tension: 0.4, pointRadius: 0 }
          ]
        },
        options: { responsive: true, maintainAspectRatio: false, plugins: { legend: { labels: { color: '#71717a', font: { size: 10 } } } }, scales: { x: { grid: { color: 'rgba(63,63,70,0.3)' }, ticks: { color: '#71717a', font: { size: 10 } } }, y: { grid: { color: 'rgba(63,63,70,0.3)' }, ticks: { color: '#71717a', font: { size: 10 } }, beginAtZero: true } } }
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

      // Initialize config
      const firstKey = this.keys.length > 0 ? this.keys[0] : null;
      this.integrationConfig = {
        base_url: this.baseURL || ('http://localhost:' + location.port + '/v1'),
        api_key: firstKey ? firstKey.key_prefix + '...' : '',
        api_key_id: firstKey ? firstKey.id : '',
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
      // Custom mode: use as-is (must start with li-)
      if (this.integrationConfig.api_key_custom) {
        return k;
      }
      // Selected from dropdown (ends with '...'): user must switch to custom mode for actual full key
      // Return prefix as-is (apply will reject if invalid)
      return k || '<YOUR_KEY>';
    },
    async applyIntegration() {
      if (!this.integrationDetail) return;
      const apiKey = this.getActualApiKey();
      if (!apiKey || apiKey === '<YOUR_KEY>' || apiKey.endsWith('...')) {
        alert('Please enter a real API key (or create one in Keys page)');
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
      if (!confirm('Remove LIAM config from ' + this.integrationDetail + '?')) return;
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
      if (this.modelSelectTarget && this.modelSelectTarget.slot) {
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
      if (!this.harvest.accounts.trim()) { alert('Paste accounts first'); return; }
      const r = await fetch('/api/harvest/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ provider: this.harvest.provider, accounts: this.harvest.accounts, concurrency: parseInt(this.harvest.concurrency), headless: this.harvest.headless === 'true' }) });
      const d = await r.json();
      if (!r.ok) { alert(d.error || 'Failed'); return; }
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
    },
    async fetchHarvestStatus() {
      try {
        const r = await fetch('/api/harvest/status');
        if (r.ok) {
          this.harvestStatus = await r.json();
          if (!this.harvestStatus.logs) this.harvestStatus.logs = [];
          if (!this.harvestStatus.accounts) this.harvestStatus.accounts = [];
          if (!this.harvestStatus.running && this.harvestStatus.success > 0) {
            this.fetchAccounts(); this.fetchStats(); this.fetchProviders(); this.buildOverviewStats();
          }
        }
      } catch (e) {}
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
    quotaPercent(a) { return a.quota_total > 0 ? Math.round((a.quota_remaining / a.quota_total) * 100) : 0; },
    quotaColor(a) { const p = this.quotaPercent(a); return p > 50 ? 'bg-ok' : p > 20 ? 'bg-warn' : 'bg-err'; },
    timeAgo(t) {
      if (!t) return '-';
      const d = Date.now() - new Date(t).getTime();
      if (d < 60000) return Math.round(d/1000) + 's ago';
      if (d < 3600000) return Math.round(d/60000) + 'm ago';
      if (d < 86400000) return Math.round(d/3600000) + 'h ago';
      return Math.round(d/86400000) + 'd ago';
    },
  };
}
