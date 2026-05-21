package rtk

import (
	"regexp"
	"strings"
)

// BuildOutput compresses build-tool output (npm / cargo / pip / maven /
// gradle / generic ERROR/WARNING). Keeps:
//
//   - errors verbatim (model needs the exact line)
//   - first 3 deprecations + count of the rest
//   - first 5 warnings + count of the rest
//   - count of "Compiling X" / "Downloading X" lines (replace verbose
//     per-package logs with a single tally)
//   - final summary lines ("added 12 packages", "Finished", "BUILD SUCCESS")
//
// Strips: per-package progress, fund-funding banners, repeating audit
// notes that don't actionably help.
//
// Cargo error blocks span multiple lines (` --> file:line`, `  | code`,
// `  = note: ...`) — we keep the whole block verbatim once an `error:` /
// `warning:` line opens it, and exit the block on the first blank line.
func BuildOutput(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 {
		return input
	}

	var errors, warnings, deprecations []string
	var summary []string
	compilingCount := 0
	downloadingCount := 0
	inCargoError := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if inCargoError {
			if trimmed == "" {
				inCargoError = false
				continue
			}
			if reCargoErrCont.MatchString(line) {
				errors = append(errors, line)
				continue
			}
			inCargoError = false
		}

		if trimmed == "" {
			continue
		}

		switch {
		case reNpmErr.MatchString(trimmed) || reYarnErr.MatchString(trimmed):
			errors = append(errors, line)
		case reNpmWarnDeprecated.MatchString(trimmed):
			deprecations = append(deprecations, line)
		case reNpmWarn.MatchString(trimmed) || reYarnWarn.MatchString(trimmed):
			warnings = append(warnings, line)
		case reGenericErrorOpen.MatchString(trimmed) || strings.HasPrefix(trimmed, "error -->"):
			errors = append(errors, line)
			inCargoError = true
		case reGenericWarningOpen.MatchString(trimmed) || strings.HasPrefix(trimmed, "warning -->"):
			warnings = append(warnings, line)
			inCargoError = true
		case reShoutErr.MatchString(trimmed):
			errors = append(errors, line)
		case reBracketErr.MatchString(trimmed) || reBuildFailed.MatchString(trimmed):
			errors = append(errors, line)
		case reBracketWarn.MatchString(trimmed):
			warnings = append(warnings, line)
		case reCompiling.MatchString(trimmed):
			compilingCount++
		case reDownloading.MatchString(trimmed) || reFetching.MatchString(trimmed):
			downloadingCount++
		case isSummaryLine(trimmed):
			summary = append(summary, line)
		}
	}

	var b strings.Builder
	keepDep := len(deprecations)
	if keepDep > deprecationKeep {
		keepDep = deprecationKeep
	}
	for i := 0; i < keepDep; i++ {
		b.WriteString(deprecations[i] + "\n")
	}
	if len(deprecations) > deprecationKeep {
		b.WriteString("... +" + itoa(len(deprecations)-deprecationKeep) + " more deprecated packages\n")
	}

	if compilingCount > 0 {
		b.WriteString("Compiled " + itoa(compilingCount) + " packages\n")
	}
	if downloadingCount > 0 {
		b.WriteString("Downloaded " + itoa(downloadingCount) + " packages\n")
	}

	for _, e := range errors {
		b.WriteString(e + "\n")
	}

	keepWarn := len(warnings)
	if keepWarn > 5 {
		keepWarn = 5
	}
	for i := 0; i < keepWarn; i++ {
		b.WriteString(warnings[i] + "\n")
	}
	if len(warnings) > 5 {
		b.WriteString("... +" + itoa(len(warnings)-5) + " more warnings\n")
	}

	for _, s := range summary {
		b.WriteString(s + "\n")
	}

	out := strings.TrimRight(b.String(), "\n")
	if out == "" {
		// Nothing meaningful classified — passthrough so the model
		// still has something to look at.
		return input
	}
	return out
}

const deprecationKeep = 3

var (
	reCargoErrCont       = regexp.MustCompile(`^\s*(-->|\||\d+\s*\||=)`)
	reNpmErr             = regexp.MustCompile(`(?i)^npm (ERR!|error)`)
	reYarnErr            = regexp.MustCompile(`(?i)^yarn error`)
	reNpmWarnDeprecated  = regexp.MustCompile(`(?i)^npm warn deprecated`)
	reNpmWarn            = regexp.MustCompile(`(?i)^npm warn`)
	reYarnWarn           = regexp.MustCompile(`(?i)^yarn warn`)
	reGenericErrorOpen   = regexp.MustCompile(`(?i)^error(\[|:)`)
	reGenericWarningOpen = regexp.MustCompile(`(?i)^warning(\[|:)`)
	reShoutErr           = regexp.MustCompile(`(?i)^ERROR:`)
	reBracketErr         = regexp.MustCompile(`(?i)^\[ERROR\]`)
	reBracketWarn        = regexp.MustCompile(`(?i)^\[WARNING\]`)
	reBuildFailed        = regexp.MustCompile(`(?i)^BUILD FAILED`)
	reCompiling          = regexp.MustCompile(`(?i)^\s*Compiling\s+\S+`)
	reDownloading        = regexp.MustCompile(`(?i)^\s*Downloading\s+\S+`)
	reFetching           = regexp.MustCompile(`(?i)^Fetching\s+`)

	reSummaryAdded     = regexp.MustCompile(`(?i)^(added|removed|changed|audited|installed)\s+\d+\s+package`)
	reSummaryFinished  = regexp.MustCompile(`(?i)^\s*Finished\s+`)
	reSummarySuccess   = regexp.MustCompile(`(?i)^BUILD SUCCESS`)
	reSummaryCount     = regexp.MustCompile(`(?i)^\d+\s+(vulnerabilities|packages?|warnings?|errors?)`)
	reSummaryInstalled = regexp.MustCompile(`(?i)^Successfully (installed|built)`)
	reSummaryToAddress = regexp.MustCompile(`(?i)^To address .* issues`)
	reSummaryRunNpm    = regexp.MustCompile(`(?i)^Run \x60npm (audit|fund)\x60`)
	reSummaryFunding   = regexp.MustCompile(`(?i)packages are looking for funding`)
)

func isSummaryLine(t string) bool {
	return reSummaryAdded.MatchString(t) ||
		reSummaryFinished.MatchString(t) ||
		reSummarySuccess.MatchString(t) ||
		reSummaryCount.MatchString(t) ||
		reSummaryInstalled.MatchString(t) ||
		reSummaryToAddress.MatchString(t) ||
		reSummaryRunNpm.MatchString(t) ||
		reSummaryFunding.MatchString(t)
}
