package rtk

import (
	"sort"
	"strings"
)

// Grep folds `grep -n` output ("file:lineno:content") into a per-file
// summary capped at GrepPerFileMax matches per file. Total match count and
// file count are surfaced up front so the model can quickly judge "is this
// useful at all".
//
// Format mirrors Rust pipe_cmd.rs grep_wrapper exactly so logs line up
// across LIAM / 9router / native rtk.
func Grep(input string) string {
	type match struct {
		lineNum string
		content string
	}
	byFile := map[string][]match{}
	total := 0

	for _, line := range strings.Split(input, "\n") {
		first := strings.IndexByte(line, ':')
		if first == -1 {
			continue
		}
		second := strings.IndexByte(line[first+1:], ':')
		if second == -1 {
			continue
		}
		second += first + 1
		file := line[:first]
		lineNumStr := line[first+1 : second]
		content := line[second+1:]

		if !isAllDigits(lineNumStr) {
			continue
		}

		total++
		byFile[file] = append(byFile[file], match{lineNumStr, content})
	}

	if total == 0 {
		return input
	}

	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	var b strings.Builder
	b.WriteString(itoa(total) + " matches in " + itoa(len(files)) + "F:\n\n")

	for _, file := range files {
		matches := byFile[file]
		b.WriteString("[file] " + file + " (" + itoa(len(matches)) + "):\n")
		limit := len(matches)
		if limit > GrepPerFileMax {
			limit = GrepPerFileMax
		}
		for i := 0; i < limit; i++ {
			m := matches[i]
			b.WriteString("  " + padLeft(m.lineNum, 4) + ": " + strings.TrimSpace(m.content) + "\n")
		}
		if len(matches) > GrepPerFileMax {
			b.WriteString("  +" + itoa(len(matches)-GrepPerFileMax) + "\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}
