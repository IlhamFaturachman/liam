package pioneer

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
)

// QuotaResult holds the billing status for a Pioneer account.
// Pioneer uses dollar-based credits (unlike AG/Kiro which use request counts),
// so we normalise to cents (multiply by 100) to fit LIAM's int-based
// QuotaTotal/QuotaRemaining fields. The dashboard renders these as "$X.XX".
type QuotaResult struct {
	Used      int                       `json:"used"`      // cents used
	Total     int                       `json:"total"`     // cents total (credit_limit or free tier)
	Remaining int                       `json:"remaining"` // cents remaining
	Plan      string                    `json:"plan"`      // "free", "hobby", "pro", "custom"
	Breakdown map[string]QuotaBreakdown `json:"breakdown,omitempty"`
}

// QuotaBreakdown holds a single entry in the breakdown map.
type QuotaBreakdown struct {
	Used  float64 `json:"used"`
	Total float64 `json:"total"`
	Label string  `json:"label,omitempty"`
}

// QuotaErrorReason classifies why a quota fetch failed.
type QuotaErrorReason string

const (
	QuotaErrorNone    QuotaErrorReason = ""
	QuotaErrorNetwork QuotaErrorReason = "network"
	QuotaErrorAuth    QuotaErrorReason = "auth_expired"
	QuotaErrorParse   QuotaErrorReason = "parse"
)

// QuotaError wraps a quota fetch failure with a structured reason.
type QuotaError struct {
	Reason  QuotaErrorReason
	Message string
}

func (e *QuotaError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Reason)
	}
	return e.Message
}

// billingStatusResponse matches Pioneer's GET /billing/billing-status JSON.
type billingStatusResponse struct {
	TotalUsage        float64 `json:"total_usage"`
	FreeTierRemaining float64 `json:"free_tier_remaining"`
	ExceedsFreeTier   bool    `json:"exceeds_free_tier"`
	HasPaymentMethod  bool    `json:"has_payment_method"`
	CreditLimit       float64 `json:"credit_limit"`
	PaymentPlan       string  `json:"payment_plan"` // "hobby", "pro", "custom"
}

// FetchQuota pulls billing status from Pioneer's API.
// Returns credit usage in cents so it fits LIAM's int-based quota fields.
//
// Pioneer returns dollar amounts:
//   - total_usage: cumulative USD spent
//   - free_tier_remaining: free credits left (only meaningful for free accounts)
//   - credit_limit: max credit cap for the plan
//
// We compute:
//   - Total  = credit_limit * 100   (cents)
//   - Used   = total_usage  * 100   (cents)
//   - Plan   = payment_plan (or "free" if no payment method + free tier remaining)
func FetchQuota(apiKey string) (*QuotaResult, error) {
	if apiKey == "" {
		return nil, &QuotaError{Reason: QuotaErrorAuth, Message: "no API key"}
	}

	req, err := http.NewRequest("GET", "https://api.pioneer.ai/billing/billing-status", nil)
	if err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: err.Error()}
	}
	req.Header.Set("X-API-Key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: err.Error()}
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			// intentionally swallowed — defer error on read-only response
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: "read body: " + err.Error()}
	}

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, &QuotaError{Reason: QuotaErrorAuth, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))}
	}
	if resp.StatusCode != 200 {
		return nil, &QuotaError{Reason: QuotaErrorNetwork, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))}
	}

	var billing billingStatusResponse
	if err := json.Unmarshal(body, &billing); err != nil {
		return nil, &QuotaError{Reason: QuotaErrorParse, Message: "parse billing: " + err.Error()}
	}

	// Determine total credit in dollars.
	// For free accounts, credit_limit may be 0 — use free_tier_remaining + total_usage
	// as the effective total (since free tier starts at $200).
	totalDollars := billing.CreditLimit
	if totalDollars <= 0 {
		// Free tier: total = used + remaining
		totalDollars = billing.TotalUsage + billing.FreeTierRemaining
		if totalDollars <= 0 {
			totalDollars = 200.0 // Pioneer free tier default
		}
	}

	// Convert to cents for LIAM's int-based fields
	totalCents := int(math.Round(totalDollars * 100))
	usedCents := int(math.Round(billing.TotalUsage * 100))
	remainingCents := totalCents - usedCents
	if remainingCents < 0 {
		remainingCents = 0
	}

	// Determine plan name
	plan := billing.PaymentPlan
	if plan == "" {
		if billing.ExceedsFreeTier {
			plan = "free (exhausted)"
		} else {
			plan = "free"
		}
	}

	// Build breakdown with a single "credits" entry showing dollar amounts
	breakdown := map[string]QuotaBreakdown{
		"credits": {
			Used:  billing.TotalUsage,
			Total: totalDollars,
			Label: "Credits ($)",
		},
	}

	return &QuotaResult{
		Used:      usedCents,
		Total:     totalCents,
		Remaining: remainingCents,
		Plan:      plan,
		Breakdown: breakdown,
	}, nil
}
