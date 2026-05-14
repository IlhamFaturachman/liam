package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

	// Per-account tracking (in-memory, fast)
	usageCount map[string][]time.Time // account_id -> timestamps of recent requests
	sessionIDs map[string]string      // account_id -> stable session ID
	stickyMap  map[string]int         // account_id -> consecutive uses count
	lastPicked string                 // last picked account_id (for sticky rotation)
}

// NewAccountPool creates a new account pool with anti-ban features
func NewAccountPool(database *db.Database, cfg *config.Config) *AccountPool {
	return &AccountPool{
		database:   database,
		cfg:        cfg,
		usageCount: make(map[string][]time.Time),
		sessionIDs: make(map[string]string),
		stickyMap:  make(map[string]int),
	}
}

// Pick selects the best available account with anti-ban protections
// Strategy:
//   1. Sticky: reuse last account if within sticky limit
//   2. Filter: only accounts that pass rate limit + min gap
//   3. Sort: LRU (least recently used first)
func (p *AccountPool) Pick(provider string) (*db.Account, error) {
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

	// Sticky: try to reuse last account if within sticky limit
	if p.cfg.StickyRequests > 0 && p.lastPicked != "" {
		for i := range accounts {
			if accounts[i].ID == p.lastPicked {
				if p.stickyMap[p.lastPicked] < p.cfg.StickyRequests {
					if p.canUse(&accounts[i], now) {
						p.recordUse(&accounts[i], now)
						return &accounts[i], nil
					}
				}
				// Sticky limit reached, reset and pick new
				p.stickyMap[p.lastPicked] = 0
				break
			}
		}
	}

	// Filter accounts that can be used right now
	var eligible []*db.Account
	for i := range accounts {
		if p.canUse(&accounts[i], now) {
			eligible = append(eligible, &accounts[i])
		}
	}

	if len(eligible) == 0 {
		// Calculate when next account becomes available
		nextAvail := p.nextAvailableTime(accounts, now)
		if nextAvail > 0 {
			return nil, fmt.Errorf("all accounts rate-limited, next available in %ds", int(nextAvail.Seconds()))
		}
		return nil, fmt.Errorf("no eligible accounts (all rate-limited or in cooldown)")
	}

	// Pick first eligible (already sorted by LRU from DB query)
	selected := eligible[0]
	p.recordUse(selected, now)

	return selected, nil
}

// GetSessionID returns a stable session ID for an account (for prompt caching)
func (p *AccountPool) GetSessionID(accountID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if sid, ok := p.sessionIDs[accountID]; ok {
		return sid
	}

	// Generate stable session ID: hash(accountID + creation_date)
	// This stays the same across restarts for the same account
	hash := sha256.Sum256([]byte(accountID + "liam-session-v1"))
	sid := hex.EncodeToString(hash[:16]) + fmt.Sprintf("%d", time.Now().UnixMilli())
	p.sessionIDs[accountID] = sid
	return sid
}

// canUse checks if an account passes rate limit + min gap checks
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

	// Check RPM (requests in last 60 seconds)
	recentCount := p.countRecentRequests(id, now, 60*time.Second)
	if recentCount >= p.cfg.AccountRPM {
		return false
	}

	return true
}

// recordUse records that an account was just used
func (p *AccountPool) recordUse(account *db.Account, now time.Time) {
	id := account.ID

	// Add timestamp
	p.usageCount[id] = append(p.usageCount[id], now)

	// Trim old timestamps (keep last 5 minutes only)
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

	// Update sticky tracking
	if p.lastPicked == id {
		p.stickyMap[id]++
	} else {
		p.lastPicked = id
		p.stickyMap[id] = 1
	}

	// Mark in DB
	p.database.MarkAccountUsed(id)
}

// countRecentRequests counts requests within a time window
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

// nextAvailableTime calculates when the next account becomes available
func (p *AccountPool) nextAvailableTime(accounts []db.Account, now time.Time) time.Duration {
	var shortest time.Duration
	for _, account := range accounts {
		timestamps := p.usageCount[account.ID]
		if len(timestamps) == 0 {
			return 0 // This account is available now (shouldn't reach here)
		}

		lastUse := timestamps[len(timestamps)-1]
		gap := time.Duration(p.cfg.AccountMinGapSec)*time.Second - now.Sub(lastUse)
		if gap <= 0 {
			return 0
		}
		if shortest == 0 || gap < shortest {
			shortest = gap
		}
	}
	return shortest
}

// CalculateCooldown returns exponential cooldown duration based on error count
func (p *AccountPool) CalculateCooldown(consecutiveErrors int) int {
	if consecutiveErrors <= 0 {
		return p.cfg.CooldownBaseSec
	}

	// Exponential: base * 2^(errors-1), capped at max
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

// Stats returns pool statistics
func (p *AccountPool) Stats() map[string]interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	return map[string]interface{}{
		"tracked_accounts": len(p.usageCount),
		"session_ids":      len(p.sessionIDs),
		"sticky_account":   p.lastPicked,
	}
}
