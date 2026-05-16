package rtk

import (
	"regexp"
	"strings"
)

// GitStatus formats `git status` output (long form OR porcelain) into a
// compact tree-oriented summary:
//
//   - main...origin/main
//   - Staged: 3 files
//     src/foo.go
//     src/bar.go
//     src/baz.go
//     ~ Modified: 1 files
//     README.md
//     ? Untracked: 0 files
//
// Long-form is what LLMs actually pass through (they grab `git status` not
// `git status --porcelain`), but we cover porcelain too in case a filter
// upstream pre-processes.
func GitStatus(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 || (len(lines) == 1 && strings.TrimSpace(lines[0]) == "") {
		return "Clean working tree"
	}

	branch := ""
	var stagedFiles, modifiedFiles, untrackedFiles []string
	staged := 0
	modified := 0
	untracked := 0
	conflicts := 0

	for _, raw := range lines {
		if strings.TrimSpace(raw) == "" {
			continue
		}

		if m := reLongBranch.FindStringSubmatch(raw); m != nil {
			branch = m[1]
			continue
		}
		if strings.HasPrefix(raw, "##") {
			branch = strings.TrimSpace(strings.TrimPrefix(raw, "##"))
			continue
		}

		// Porcelain status: 2-char prefix + space + path
		if len(raw) >= 3 && rePorcelainPrefix.MatchString(raw[:2]) && raw[2] == ' ' {
			x := raw[0]
			y := raw[1]
			file := raw[3:]

			if raw[:2] == "??" {
				untracked++
				untrackedFiles = append(untrackedFiles, file)
				continue
			}
			if strings.ContainsRune("MADRC", rune(x)) {
				staged++
				stagedFiles = append(stagedFiles, file)
			} else if x == 'U' {
				conflicts++
			}
			if y == 'M' || y == 'D' {
				modified++
				modifiedFiles = append(modifiedFiles, file)
			}
			continue
		}

		// Long-form: "modified:   path", "new file:   path", ...
		if m := reLongStatus.FindStringSubmatch(raw); m != nil {
			kind := m[1]
			path := strings.TrimSpace(m[2])
			switch kind {
			case "both modified":
				conflicts++
			case "modified", "deleted":
				modified++
				modifiedFiles = append(modifiedFiles, path)
			case "new file", "renamed":
				staged++
				stagedFiles = append(stagedFiles, path)
			}
		}
	}

	var b strings.Builder
	if branch != "" {
		b.WriteString("* " + branch + "\n")
	}

	writeSection(&b, "+ Staged", staged, stagedFiles, StatusMaxFiles)
	writeSection(&b, "~ Modified", modified, modifiedFiles, StatusMaxFiles)
	writeSection(&b, "? Untracked", untracked, untrackedFiles, StatusMaxUntracked)

	if conflicts > 0 {
		b.WriteString("conflicts: " + itoa(conflicts) + " files\n")
	}
	if staged == 0 && modified == 0 && untracked == 0 && conflicts == 0 {
		b.WriteString("clean — nothing to commit\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

func writeSection(b *strings.Builder, label string, count int, files []string, max int) {
	if count == 0 {
		return
	}
	b.WriteString(label + ": " + itoa(count) + " files\n")
	limit := len(files)
	if limit > max {
		limit = max
	}
	for i := 0; i < limit; i++ {
		b.WriteString("   " + files[i] + "\n")
	}
	if len(files) > max {
		b.WriteString("   ... +" + itoa(len(files)-max) + " more\n")
	}
}

var (
	reLongBranch      = regexp.MustCompile(`^On branch (\S+)`)
	rePorcelainPrefix = regexp.MustCompile(`^[ MADRCU?!][ MADRCU?!]$`)
	reLongStatus      = regexp.MustCompile(`^\s*(modified|new file|deleted|renamed|both modified):\s+(.+)$`)
)
