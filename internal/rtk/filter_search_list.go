package rtk

import (
	"regexp"
	"sort"
	"strings"
)

// SearchList compresses Cursor's Glob tool output:
//
//	Result of search in '/path' (total 47 files):
//	- foo/bar.go
//	- foo/baz.go
//	...
//
// Output groups by parent dir like Find does, with caps to keep the
// summary readable.
func SearchList(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 {
		return input
	}

	header := ""
	if len(lines) > 0 {
		header = lines[0]
	}
	rest := lines[1:]

	var paths []string
	for _, raw := range rest {
		t := strings.TrimSpace(raw)
		if !strings.HasPrefix(t, "- ") {
			continue
		}
		paths = append(paths, strings.TrimPrefix(t, "- "))
	}
	if len(paths) == 0 {
		return input
	}

	byDir := map[string][]string{}
	for _, p := range paths {
		dir, base := splitPath(p)
		byDir[dir] = append(byDir[dir], base)
	}

	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var b strings.Builder
	b.WriteString(header + "\n")
	b.WriteString(itoa(len(paths)) + " files in " + itoa(len(dirs)) + " dirs:\n\n")

	dirLimit := len(dirs)
	if dirLimit > SearchListTotalDirMax {
		dirLimit = SearchListTotalDirMax
	}
	for i := 0; i < dirLimit; i++ {
		dir := dirs[i]
		names := byDir[dir]
		b.WriteString(dir + "/ (" + itoa(len(names)) + "):\n")
		fileLimit := len(names)
		if fileLimit > SearchListPerDirMax {
			fileLimit = SearchListPerDirMax
		}
		for j := 0; j < fileLimit; j++ {
			b.WriteString("  " + names[j] + "\n")
		}
		if len(names) > SearchListPerDirMax {
			b.WriteString("  +" + itoa(len(names)-SearchListPerDirMax) + "\n")
		}
		b.WriteString("\n")
	}
	if len(dirs) > SearchListTotalDirMax {
		b.WriteString("+" + itoa(len(dirs)-SearchListTotalDirMax) + " more dirs\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// SearchListHeaderRe is exported so autodetect can sniff the input
// shape without duplicating the regex.
var SearchListHeaderRe = regexp.MustCompile(`^Result of search in '[^']*' \(total \d+ files?\):`)
