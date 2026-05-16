package rtk

import (
	"strings"
)

// GitDiff is the Go port of 9router gitDiff (which itself ports Rust
// git::compact_diff). It compacts a unified diff:
//
//   - Each file gets a header (path) and a "+N -M" tally line.
//   - Each hunk gets capped at GitDiffHunkMaxLines lines; further +/-/context
//     lines are counted but emitted only as "(N lines truncated)".
//   - Total output capped at maxLines (default 500) so a 50-file diff can't
//     blow past the budget.
//
// Why "compact" not "summarise": the LLM still needs the actual changed
// lines to reason about correctness. Dropping context but keeping +/- is
// the right trade-off; full summary loses too much.
func GitDiff(diff string) string {
	return gitDiffWithCap(diff, 500)
}

func gitDiffWithCap(diff string, maxLines int) string {
	var result []string
	currentFile := ""
	added := 0
	removed := 0
	inHunk := false
	hunkShown := 0
	hunkSkipped := 0
	wasTruncated := false
	maxHunkLines := GitDiffHunkMaxLines

	lines := strings.Split(diff, "\n")

outer:
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git"):
			if hunkSkipped > 0 {
				result = append(result, sprintTruncated(hunkSkipped))
				wasTruncated = true
				hunkSkipped = 0
			}
			if currentFile != "" && (added > 0 || removed > 0) {
				result = append(result, sprintTally(added, removed))
			}
			parts := strings.SplitN(line, " b/", 2)
			if len(parts) > 1 {
				currentFile = parts[1]
			} else {
				currentFile = "unknown"
			}
			result = append(result, "\n"+currentFile)
			added = 0
			removed = 0
			inHunk = false
			hunkShown = 0

		case strings.HasPrefix(line, "@@"):
			if hunkSkipped > 0 {
				result = append(result, sprintTruncated(hunkSkipped))
				wasTruncated = true
				hunkSkipped = 0
			}
			inHunk = true
			hunkShown = 0
			result = append(result, "  "+line)

		case inHunk:
			switch {
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				added++
				if hunkShown < maxHunkLines {
					result = append(result, "  "+line)
					hunkShown++
				} else {
					hunkSkipped++
				}
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				removed++
				if hunkShown < maxHunkLines {
					result = append(result, "  "+line)
					hunkShown++
				} else {
					hunkSkipped++
				}
			case hunkShown < maxHunkLines && !strings.HasPrefix(line, "\\"):
				if hunkShown > 0 {
					// Mirror JS: only emit context after at least one +/- has
					// landed, so leading context (which the model already
					// has from the file dump) doesn't bloat the hunk.
					result = append(result, "  "+line)
					hunkShown++
				}
			}
		}

		if len(result) >= maxLines {
			result = append(result, "\n... (more changes truncated)")
			wasTruncated = true
			break outer
		}
	}

	if hunkSkipped > 0 {
		result = append(result, sprintTruncated(hunkSkipped))
		wasTruncated = true
	}
	if currentFile != "" && (added > 0 || removed > 0) {
		result = append(result, sprintTally(added, removed))
	}
	if wasTruncated {
		result = append(result, "[full diff: rtk git diff --no-compact]")
	}

	return strings.Join(result, "\n")
}

func sprintTruncated(n int) string {
	return "  ... (" + itoa(n) + " lines truncated)"
}

func sprintTally(added, removed int) string {
	return "  +" + itoa(added) + " -" + itoa(removed)
}

// itoa avoids the strconv import on filter hot paths. Filter packages each
// inline this — Go's compiler will dedup at link time and the surface area
// per filter stays tiny.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
