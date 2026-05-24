package pioneer

// PioneerCredentials holds the API key for a Pioneer account.
// Pioneer keys are long-lived (up to 1 year) and do not require
// refresh — the only credential is the API key itself.
type PioneerCredentials struct {
	APIKey string `json:"api_key"`
}
