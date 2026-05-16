package rtk

import (
	"log"
)

// Filter is a stateless string transform. The output should be a strict
// compression of the input — same domain, fewer bytes — so the LLM's
// reasoning about the tool result remains valid even though some lines
// were dropped or summarised.
//
// Filters MUST be deterministic for the same input. We rely on that to
// reason about reproducibility when troubleshooting "why did this prompt
// behave differently between two requests".
type Filter func(string) string

// FilterFunc bundles a Filter with its canonical name for log output.
// Splitting the type name from the function lets us print which filter
// fired without resorting to runtime reflection.
type FilterFunc struct {
	Name string
	Fn   Filter
}

// SafeApply runs a filter and recovers from any panic, returning the
// original text unchanged. Mirrors the Rust catch_unwind / JS try-catch
// guard in the upstream port.
//
// We log the recovery loudly because a panicking filter is a real bug
// (regex catastrophic backtrack, division by zero in line ratio math,
// etc.) but we never want a token saver to take down a request — the
// model can still answer with the raw output.
func SafeApply(ff FilterFunc, text string) string {
	if ff.Fn == nil {
		return text
	}
	out := text
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[rtk] warning: filter %q panicked — passing through raw output: %v", ff.Name, r)
			out = text
		}
	}()
	out = ff.Fn(text)
	return out
}
