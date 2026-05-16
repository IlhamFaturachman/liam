package rtk

import (
	"strings"
)

// DedupLog is the generic fallback compressor: collapse consecutive
// duplicate lines into "(N duplicate lines)" markers, dedupe blank
// streaks (multiple blank lines in a row become at most one blank line),
// and hard-cap total output at DedupLineMax lines so a 100k-line log
// can't dominate the prompt.
//
// Design notes:
//
//   - We do NOT rearrange lines. Order is preserved; only adjacent
//     duplicates collapse. This keeps the surrounding context the model
//     uses to interpret the log intact.
//   - Blank lines between duplicate groups get squeezed but not removed
//     — a blank-line break still signals "different log section" to the
//     reader, which is information the model uses too.
//   - The truncation marker is emitted in-line so the model knows it's
//     looking at a partial log, not a complete one.
func DedupLog(input string) string {
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	prev := ""
	prevSet := false
	runCount := 0
	blankStreak := 0

	flushRun := func() {
		if prevSet && runCount > 1 {
			out = append(out, "  ... ("+itoa(runCount-1)+" duplicate lines)")
		}
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blankStreak < 1 {
				out = append(out, line)
			}
			blankStreak++
			flushRun()
			prev = ""
			prevSet = false
			runCount = 0
			continue
		}
		blankStreak = 0
		if prevSet && line == prev {
			runCount++
			continue
		}
		flushRun()
		out = append(out, line)
		prev = line
		prevSet = true
		runCount = 1
		if len(out) >= DedupLineMax {
			out = append(out, "... (truncated at "+itoa(DedupLineMax)+" lines)")
			return strings.Join(out, "\n")
		}
	}
	flushRun()
	return strings.Join(out, "\n")
}
