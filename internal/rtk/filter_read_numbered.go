package rtk

import (
	"regexp"
	"strings"
)

// ReadNumbered handles "  N|content" file dumps (Cursor / Codex
// read_file output). When the file is long, we keep the first HEAD
// lines + last TAIL lines and replace the middle, same as
// SmartTruncate, but with a different marker so the model knows the
// content is a single file (not a generic truncated log).
//
// Detection (in autodetect): >= 70% of sample lines match the
// "N|content" shape AND total lines exceed SMART_TRUNCATE_MIN_LINES.
func ReadNumbered(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < SmartTruncateMinLines {
		return input
	}

	head := lines[:SmartTruncateHead]
	tail := lines[len(lines)-SmartTruncateTail:]
	cut := len(lines) - len(head) - len(tail)

	out := make([]string, 0, len(head)+1+len(tail))
	out = append(out, head...)
	out = append(out, "... +"+itoa(cut)+" lines truncated (file continues)")
	out = append(out, tail...)
	return strings.Join(out, "\n")
}

// ReadNumberedLineRe is exported so autodetect can sample lines without
// duplicating the rule.
var ReadNumberedLineRe = regexp.MustCompile(`^\s*\d+[|:]`)
