package rtk

import (
	"strings"
)

// Tree strips the "5 directories, 23 files" summary line that `tree`
// appends, drops leading/trailing blank lines, and caps absurdly long
// trees at TreeMaxLines (Rust port has no cap, but we add one to keep
// pathological `tree -a` from blowing the prompt).
func Tree(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 {
		return input
	}

	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		// Drop "X directories, Y files" summary.
		if strings.Contains(line, "director") && strings.Contains(line, "file") {
			continue
		}
		// Drop leading blanks.
		if strings.TrimSpace(line) == "" && len(filtered) == 0 {
			continue
		}
		filtered = append(filtered, line)
	}

	// Drop trailing blanks.
	for len(filtered) > 0 && strings.TrimSpace(filtered[len(filtered)-1]) == "" {
		filtered = filtered[:len(filtered)-1]
	}

	if len(filtered) > TreeMaxLines {
		cut := len(filtered) - TreeMaxLines
		head := strings.Join(filtered[:TreeMaxLines], "\n")
		return head + "\n... +" + itoa(cut) + " more lines"
	}

	return strings.Join(filtered, "\n")
}
