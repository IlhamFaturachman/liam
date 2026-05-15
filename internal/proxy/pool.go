package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/liam-auto/liam/internal/config"
	"github.com/liam-auto/liam/internal/db"
)

// AccountPool manages account selection with anti-ban protections
type AccountPool struct {
	database *db.Database
	cfg      *config.Config
	mu       sync.Mutex

	// Strategy config (loaded from DB settings)
	strategy    string // "fill-first" | "round-robin"
	stickyLimit int
	providerOverrides map[string]providerStrategy

	// Per-account tracking (in-memory, fast)
	usageCount map[string][]time.Time
	sessionIDs map[string]string
}

type providerStrategy struct {
	Strategy    string `json:"strategy"`
	StickyLimit int    `json:"sticky_limit"`
}

// NewAccountPool creates a new account pool with anti-ban features
func NewAccountPool(database *db.Database, cfg *config.Config) *AccountPool {
	p := &AccountPool{
		database:          database,
		cfg:               cfg,
		usageCount:        make(map[string][]time.Time),
		sessionIDs:        make(map[string]string),
		providerOverrides: make(map[string]providerStrategy),
		strategy:          "round-robin",
		stickyLimit:       cfg.StickyRequests,
	}
	p.UpdateStrategy(database)
	return p
}

// UpdateStrategy reloads strategy settings from DB
func (p *AccountPool) UpdateStrategy(database *db.Database) {
	p.mu.Lock()
	defer p.mu.Unlock()

	strategy := database.GetSetting("routing_strategy", "round-robin")
	stickyStr := database.GetSetting("routing_sticky_limit", strconv.Itoa(p.cfg.StickyRequests))
	overridesJSON := database.GetSetting("routing_provider_overrides", "{}")

	p.strategy = strategy
	p.stickyLimit, _ = strconv.Atoi(stickyStr)
	if p.stickyLimit <= 0 {
		p.stickyLimit = 3
	}

	var overrides map[string]providerStrategy
	json.Unmarshal([]byte(overridesJSON), &overrides)
	if overrides != nil {
		p.providerOverrides = overrides
	}
}

// getProviderStrategy returns the strategy for a specific provider (override → global)
func (p *AccountPool) getProviderStrategy(provider string) (string, int) {
	if override, ok := p.providerOverrides[provider]; ok {
		strategy := override.Strategy
		sticky := override.StickyLimit
		if strategy == "" {
			strategy = p.strategy
		}
		if sticky <= 0 {
			sticky = p.stickyLimit
		}
		return strategy, sticky
	}
	return p.strategy, p.stickyLimit
}

// Pick selects the best available account with anti-ban protections + per-model lock
func (p *AccountPool) Pick(provider string) (*db.Account, error) {
	return p.PickForModel(provider, "")
}

// PickForModel selects account considering per-model locks
func (p *AccountPool) PickForModel(provider, model string) (*db.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	accounts, err := p.database.GetActiveAccounts(provider)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts: %w", err)
	}

	if len(accounts) == 0 {
		return nil, fmt.Errorf("no active accounts for provider '%s'", provider)
	}

	now := time.Now()
	strategy, stickyLimit := p.getProviderStrategy(provider)

	// Filter: rate limit + min gap + per-model lock
	var eligible []*db.Account
	for i := range accounts {
		if !p.canUse(&accounts[i], now) {
			continue
		}
		// Per-model lock check
		if model != "" && p.isModelLocked(&accounts[i], model, now) {
			continue
		}
		eligible = append(eligible, &accounts[i])
	}

	if len(eligible) == 0 {
		return nil, fmt.Errorf("all accounts rate-limited or model-locked for '%s'", provider)
	}

	var selected *db.Account

	switch strategy {
	case "fill-first":
		// Pick highest priority (lowest number, 0 = unset treated as last)
		selected = eligible[0]
		for _, a := range eligible[1:] {
			aPri := p.getAccountPriority(a)
			sPri := p.getAccountPriority(selected)
			if aPri < sPri || (aPri == sPri && p.isLessRecentlyUsed(a, selected)) {
				selected = a
			}
		}

	case "round-robin":
		// Find most recently used (sticky candidate)
		var mostRecent *db.Account
		for _, a := range eligible {
			if mostRecent == nil {
				mostRecent = a
			} else if a.LastUsedAt != nil && (mostRecent.LastUsedAt == nil || a.LastUsedAt.After(*mostRecent.LastUsedAt)) {
				mostRecent = a
			}
		}

		if mostRecent != nil && mostRecent.LastUsedAt != nil {
			_, count, _ := p.database.GetAccountWithDetails(mostRecent.ID)
			if count < stickyLimit {
				// Stay with current (sticky)
				selected = mostRecent
				p.database.UpdateConsecutiveUseCount(mostRecent.ID, count+1)
			} else {
				// Rotate to least recently used
				selected = p.leastRecentlyUsed(eligible)
				p.database.UpdateConsecutiveUseCount(selected.ID, 1)
			}
		} else {
			// No usage history, pick first by priority
			selected = eligible[0]
			p.database.UpdateConsecutiveUseCount(selected.ID, 1)
		}

	default:
		// Default to LRU (backward compat)
		selected = eligible[0]
	}

	// Record use
	p.recordUse(selected, now)
	return selected, nil
}

// GetSessionID returns a stable session ID for an account
func (p *AccountPool) GetSessionID(accountID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sid, ok := p.sessionIDs[accountID]; ok {
		return sid
	}

	hash := sha256.Sum256([]byte(accountID + "liam-session-v1"))
	sid := hex.EncodeToString(hash[:16]) + fmt.Sprintf("%d", time.Now().UnixMilli())
	p.sessionIDs[accountID] = sid
	return sid
}

// CalculateCooldown returns exponential cooldown duration based on error count
func (p *AccountPool) CalculateCooldown(consecutiveErrors int) int {
	if consecutiveErrors <= 0 {
		return p.cfg.CooldownBaseSec
	}
	cooldown := p.cfg.CooldownBaseSec
	for i := 1; i < consecutiveErrors; i++ {
		cooldown *= 2
		if cooldown >= p.cfg.CooldownMaxSec {
			return p.cfg.CooldownMaxSec
		}
	}
	return cooldown
}

// ShouldDisable checks if account should be permanently disabled
func (p *AccountPool) ShouldDisable(consecutiveErrors int) bool {
	return consecutiveErrors >= p.cfg.DisableAfterErrors
}

// Count returns the number of active accounts for a provider
func (p *AccountPool) Count(provider string) int {
	accounts, err := p.database.GetActiveAccounts(provider)
	if err != nil {
		return 0
	}
	return len(accounts)
}

// --- Internal helpers ---

func (p *AccountPool) canUse(account *db.Account, now time.Time) bool {
	id := account.ID

	// Check minimum gap since last use
	timestamps := p.usageCount[id]
	if len(timestamps) > 0 {
		lastUse := timestamps[len(timestamps)-1]
		gap := now.Sub(lastUse)
		if gap < time.Duration(p.cfg.AccountMinGapSec)*time.Second {
			return false
		}
	}

	// Check RPM
	recentCount := p.countRecentRequests(id, now, 60*time.Second)
	if recentCount >= p.cfg.AccountRPM {
		return false
	}

	return true
}

func (p *AccountPool) isModelLocked(account *db.Account, model string, now time.Time) bool {
	locks := p.database.GetModelLocks(account.ID)
	if until, ok := locks[model]; ok {
		return until.After(now)
	}
	return false
}

func (p *AccountPool) getAccountPriority(account *db.Account) int {
	pri, _, _ := p.database.GetAccountWithDetails(account.ID)
	if pri <= 0 {
		return 9999 // Unset priority = lowest
	}
	return pri
}

func (p *AccountPool) isLessRecentlyUsed(a, b *db.Account) bool {
	if a.LastUsedAt == nil {
		return true
	}
	if b.LastUsedAt == nil {
		return false
	}
	return a.LastUsedAt.Before(*b.LastUsedAt)
}

func (p *AccountPool) leastRecentlyUsed(accounts []*db.Account) *db.Account {
	lru := accounts[0]
	for _, a := range accounts[1:] {
		if p.isLessRecentlyUsed(a, lru) {
			lru = a
		}
	}
	return lru
}

func (p *AccountPool) recordUse(account *db.Account, now time.Time) {
	id := account.ID

	p.usageCount[id] = append(p.usageCount[id], now)

	// Trim old timestamps (keep last 5 minutes)
	cutoff := now.Add(-5 * time.Minute)
	timestamps := p.usageCount[id]
	trimIdx := 0
	for i, t := range timestamps {
		if t.After(cutoff) {
			trimIdx = i
			break
		}
	}
	if trimIdx > 0 {
		p.usageCount[id] = timestamps[trimIdx:]
	}

	p.database.MarkAccountUsed(id)
}

func (p *AccountPool) countRecentRequests(accountID string, now time.Time, window time.Duration) int {
	timestamps := p.usageCount[accountID]
	cutoff := now.Add(-window)
	count := 0
	for _, t := range timestamps {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}
