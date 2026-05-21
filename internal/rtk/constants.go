// Package rtk implements LLM token savers ported from 9router/open-sse/rtk.
//
// RTK (originally a Rust CLI by Julius Brussee, ported by 9router to JS, and
// here ported to Go) compresses tool_result content in LLM request bodies
// before they reach the upstream provider. Typical savings are 20-40% input
// tokens on agentic-coding traffic where tools like git-diff / grep / ls
// produce verbose multi-kilobyte outputs that the model rarely needs in full.
//
// Design notes for LIAM:
//
//  1. RTK operates on OpenAI canonical shape (the wire format clients speak
//     to LIAM). It runs BEFORE any provider-specific translateRequest. That
//     makes it provider-agnostic by construction: Kiro, Antigravity, or any
//     future provider plugged into LIAM gets compressed bodies without
//     knowing RTK exists.
//
//  2. Supported message shapes mirror what 9router covers:
//     a. {role:"tool", content:"text"}                                — OpenAI chat completion
//     b. {role:"tool", content:[{type:"text", text:"..."}]}            — OpenAI array form
//     c. {role:"user", content:[{type:"tool_result", content:"..."}]}  — Anthropic-style inside user
//     d. {role:"user", content:[{type:"tool_result", content:[{type:"text",text}]}]} — Anthropic array
//
//     Tool-call ARGUMENTS (the JSON streaming side that suffered the
//     truncation bug in Session N+2) are NOT touched by RTK — only
//     tool_result content.
//
//  3. is_error=true tool_results are preserved as-is. Compressing an error
//     trace can hide the very signal the model needs to recover.
//
//  4. Filters are panic-safe. A buggy filter passes through raw content
//     instead of producing empty output.
//
//  5. Public API surface is intentionally tiny: rtk.Compress(body, enabled)
//     on a parsed map[string]any. Callers re-marshal the (mutated) body.
package rtk

// Cap constants mirror the original Rust defaults via the JS port. Kept in
// one place so operators troubleshooting "why did RTK skip my output" can
// see the thresholds at a glance.
const (
	// RawCap is the upper size beyond which RTK stops compressing. Past
	// this, the input is almost certainly not a tool output we recognise
	// (uploaded file, image dump, etc.) and the autodetect cost of running
	// 11 regexes on multi-MB strings is wasted.
	RawCap = 10 * 1024 * 1024 // 10 MiB

	// MinCompressSize: skip blobs smaller than this. RTK's structural
	// filters need enough lines to detect a pattern; anything tinier won't
	// recover meaningful tokens.
	MinCompressSize = 500

	// DetectWindow caps how many bytes autodetect peeks at when picking
	// a filter. The actual filter still operates on the full input.
	DetectWindow = 1024

	// GitDiffHunkMaxLines: per-hunk cap inside gitDiff. After this we
	// emit "(N lines truncated)" and stop appending to that hunk.
	GitDiffHunkMaxLines = 100

	// DedupLineMax: dedupLog truncation cap on the number of lines emitted.
	DedupLineMax = 2000

	// GrepPerFileMax mirrors Rust pipe_cmd.rs `matches.iter().take(10)`:
	// for each file, only keep the first 10 grep hits.
	GrepPerFileMax = 10

	// FindPerDirMax / FindTotalDirMax mirror the Rust caps for `find`.
	FindPerDirMax   = 10
	FindTotalDirMax = 20

	// StatusMaxFiles / StatusMaxUntracked mirror config::limits() in Rust.
	StatusMaxFiles     = 10
	StatusMaxUntracked = 10

	// LsExtSummaryTop: top-N file extensions surfaced in the ls compact
	// summary line.
	LsExtSummaryTop = 5

	// TreeMaxLines: no native cap in Rust; we add one because pathological
	// `tree -a` output can blow into thousands of lines.
	TreeMaxLines = 200

	// SearchListPerDirMax / SearchListTotalDirMax: caps for Cursor's
	// "Result of search in '...'" list.
	SearchListPerDirMax   = 10
	SearchListTotalDirMax = 20

	// SmartTruncate head/tail: when no structural filter matches, we
	// fall back to keeping the first N lines and the last M lines and
	// dropping the middle.
	SmartTruncateHead     = 120
	SmartTruncateTail     = 60
	SmartTruncateMinLines = 250

	// ReadNumberedMinHitRatio: a blob is treated as a line-numbered file
	// dump when at least this fraction of sample lines match the
	// "  N|content" pattern.
	ReadNumberedMinHitRatio = 0.7
)

// LsNoiseDirs are directory names hidden from `ls` output as they're
// almost never the answer to "what's in this folder" — they're build
// artifacts or dependency caches that bloat output.
var LsNoiseDirs = []string{
	"node_modules", ".git", "target", "__pycache__",
	".next", "dist", "build", ".venv", "venv",
	".cache", ".idea", ".vscode", ".DS_Store",
}

// FilterName is the canonical identifier for a filter, matching the Rust /
// JS port one-for-one so logs are cross-greppable.
const (
	FilterGitDiff       = "git-diff"
	FilterGitStatus     = "git-status"
	FilterGrep          = "grep"
	FilterFind          = "find"
	FilterLs            = "ls"
	FilterTree          = "tree"
	FilterDedupLog      = "dedup-log"
	FilterSmartTruncate = "smart-truncate"
	FilterBuildOutput   = "build-output"
	FilterReadNumbered  = "read-numbered"
	FilterSearchList    = "search-list"
)
