package rtk

import (
	"regexp"
	"strings"
)

// AutoDetect picks the best-fit filter for `text` by sniffing the first
// DetectWindow bytes. Returns FilterFunc{Name:"", Fn:nil} when no filter
// matches — the caller should pass through the raw text in that case.
//
// Order matters. The first match wins, so cheaper/more specific patterns
// run before general ones:
//
//  1. git-diff (unified diff hunks)
//  2. git-status (long-form OR porcelain)
//  3. build-output (npm / cargo / pip / maven) — BEFORE porcelain so
//     "Compiling foo" doesn't get misread as a status entry
//  4. porcelain ratio fallback (handles porcelain without long-form)
//  5. grep (file:line:content)
//  6. find (path-like, no colons)
//  7. tree (box-drawing glyphs)
//  8. ls (perm chars OR "total N" header)
//  9. search-list (Cursor Glob header)
//  10. read-numbered (high hit ratio of "  N|content")
//  11. dedup-log (5+ non-empty lines, generic noise fallback)
//  12. smart-truncate (last resort: very long, no structure)
func AutoDetect(text string) FilterFunc {
	head := text
	if len(head) > DetectWindow {
		head = head[:DetectWindow]
	}

	if reGitDiff.MatchString(head) || reGitDiffHunk.MatchString(head) {
		return FilterFunc{Name: FilterGitDiff, Fn: GitDiff}
	}
	if reGitStatus.MatchString(head) {
		return FilterFunc{Name: FilterGitStatus, Fn: GitStatus}
	}

	// Build output BEFORE porcelain check: prevents cargo "Compiling"
	// being misdetected as `git status` porcelain prefix.
	if reBuildOutput.MatchString(head) {
		return FilterFunc{Name: FilterBuildOutput, Fn: BuildOutput}
	}

	if isMostlyPorcelain(head) {
		return FilterFunc{Name: FilterGitStatus, Fn: GitStatus}
	}

	lines := strings.Split(head, "\n")
	var nonEmpty []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}

	// Grep: first 5 non-empty lines, ANY matches "file:number:content".
	first5 := nonEmpty
	if len(first5) > 5 {
		first5 = first5[:5]
	}
	for _, l := range first5 {
		if isGrepLine(l) {
			return FilterFunc{Name: FilterGrep, Fn: Grep}
		}
	}

	// Find: ALL non-empty lines path-like (no colon), >= 3 lines.
	if len(nonEmpty) >= 3 {
		allPathLike := true
		for _, l := range nonEmpty {
			if !isPathLike(l) {
				allPathLike = false
				break
			}
		}
		if allPathLike {
			return FilterFunc{Name: FilterFind, Fn: Find}
		}
	}

	if reTreeGlyph.MatchString(head) {
		return FilterFunc{Name: FilterTree, Fn: Tree}
	}

	if reLsTotal.MatchString(head) || countMatches(head, reLsRow) >= 3 {
		return FilterFunc{Name: FilterLs, Fn: Ls}
	}

	if SearchListHeaderRe.MatchString(head) {
		return FilterFunc{Name: FilterSearchList, Fn: SearchList}
	}

	// read-numbered uses full-text line count (not the head-window)
	// because Cursor file dumps frequently exceed 1024 chars in their
	// header alone, so a head-only sample undercounts. Hit ratio still
	// runs on the head sample which is enough to confirm shape.
	fullLineCount := len(strings.Split(text, "\n"))
	if fullLineCount >= SmartTruncateMinLines && isLineNumbered(lines) {
		return FilterFunc{Name: FilterReadNumbered, Fn: ReadNumbered}
	}

	if len(nonEmpty) >= 5 {
		return FilterFunc{Name: FilterDedupLog, Fn: DedupLog}
	}

	if fullLineCount >= SmartTruncateMinLines {
		return FilterFunc{Name: FilterSmartTruncate, Fn: SmartTruncate}
	}

	return FilterFunc{}
}

func isGrepLine(line string) bool {
	first := strings.IndexByte(line, ':')
	if first == -1 {
		return false
	}
	second := strings.IndexByte(line[first+1:], ':')
	if second == -1 {
		return false
	}
	second += first + 1
	lineNum := line[first+1 : second]
	if lineNum == "" {
		return false
	}
	for i := 0; i < len(lineNum); i++ {
		c := lineNum[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isPathLike(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if strings.Contains(t, ":") {
		return false
	}
	return strings.HasPrefix(t, ".") || strings.HasPrefix(t, "/") || strings.Contains(t, "/")
}

// isMostlyPorcelain returns true when >= 60% of non-empty lines start
// with the 2-character porcelain status prefix. Threshold mirrors the
// Rust port; it's tuned to catch "git status --porcelain" output that
// arrives without the long-form "On branch ..." header.
func isMostlyPorcelain(head string) bool {
	var lines []string
	for _, l := range strings.Split(head, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 3 {
		return false
	}
	hits := 0
	for _, l := range lines {
		if rePorcelainLine.MatchString(l) {
			hits++
		}
	}
	return float64(hits)/float64(len(lines)) >= 0.6
}

func isLineNumbered(lines []string) bool {
	hits := 0
	nonEmpty := 0
	sample := lines
	if len(sample) > 100 {
		sample = sample[:100]
	}
	for _, l := range sample {
		if l == "" {
			continue
		}
		nonEmpty++
		if ReadNumberedLineRe.MatchString(l) {
			hits++
		}
	}
	if nonEmpty < 5 {
		return false
	}
	return float64(hits)/float64(nonEmpty) >= ReadNumberedMinHitRatio
}

func countMatches(text string, re *regexp.Regexp) int {
	return len(re.FindAllStringIndex(text, -1))
}

var (
	reGitDiff       = regexp.MustCompile(`(?m)^diff --git `)
	reGitDiffHunk   = regexp.MustCompile(`(?m)^@@ `)
	reGitStatus     = regexp.MustCompile(`(?m)^On branch |^nothing to commit|^Changes (not |to be )|^Untracked files:`)
	rePorcelainLine = regexp.MustCompile(`^[ MADRCU?!][ MADRCU?!] \S`)
	reBuildOutput   = regexp.MustCompile(`(?im)^(npm (warn|error|ERR!)|yarn (warn|error)|\s*Compiling\s+\S+|\s*Downloading\s+\S+|added \d+ package|\[ERROR\]|BUILD (SUCCESS|FAILED)|\s*Finished\s+|Successfully (installed|built)|ERROR:)`)
	reTreeGlyph     = regexp.MustCompile(`[├└]──|│  `)
	reLsRow         = regexp.MustCompile(`(?m)^[-dlbcps][rwx-]{9}`)
	reLsTotal       = regexp.MustCompile(`(?m)^total \d+$`)
)
