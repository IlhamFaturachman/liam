package rtk

import (
	"strings"
)

// SmartTruncate is the last-resort filter: input has no recognisable
// structure but is too long to ship verbatim, so we keep the first HEAD
// lines and the last TAIL lines and replace the middle with a "+N lines
// truncated" marker.
//
// Why head + tail (not just head): the model often needs the END of a
// log to see the most recent error, and the START to see the command
// that produced it. Cutting the middle preserves both signals.
//
// Threshold: only kicks in at SMART_TRUNCATE_MIN_LINES (250). Below that,
// shipping the whole thing is cheaper than running this filter and gives
// the model more to work with.
func SmartTruncate(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < SmartTruncateMinLines {
		return input
	}

	head := lines[:SmartTruncateHead]
	tail := lines[len(lines)-SmartTruncateTail:]
	cut := len(lines) - len(head) - len(tail)

	out := make([]string, 0, len(head)+1+len(tail))
	out = append(out, head...)
	out = append(out, "... +"+itoa(cut)+" lines truncated")
	out = append(out, tail...)
	return strings.Join(out, "\n")
}
