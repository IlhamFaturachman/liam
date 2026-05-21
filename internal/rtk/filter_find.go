package rtk

import (
	"sort"
	"strings"
)

// Find groups bare path output (typical of `find . -type f`) by parent
// directory, capping basenames-per-dir and total dirs to keep the summary
// readable. Mirrors Rust pipe_cmd.rs find_wrapper exactly.
//
// Detection trigger (in autodetect): >= 3 non-empty lines AND every line
// is path-like AND no line contains a colon. So we're confident the input
// is plain paths, not grep output.
func Find(input string) string {
	var paths []string
	for _, line := range strings.Split(input, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		paths = append(paths, line)
	}
	if len(paths) == 0 {
		return input
	}

	byDir := map[string][]string{}
	for _, path := range paths {
		dir, base := splitPath(path)
		byDir[dir] = append(byDir[dir], base)
	}

	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var b strings.Builder
	b.WriteString(itoa(len(paths)) + " files in " + itoa(len(dirs)) + " dirs:\n\n")

	dirLimit := len(dirs)
	if dirLimit > FindTotalDirMax {
		dirLimit = FindTotalDirMax
	}
	for i := 0; i < dirLimit; i++ {
		dir := dirs[i]
		files := byDir[dir]
		b.WriteString(dir + "/ (" + itoa(len(files)) + "):\n")
		fileLimit := len(files)
		if fileLimit > FindPerDirMax {
			fileLimit = FindPerDirMax
		}
		for j := 0; j < fileLimit; j++ {
			b.WriteString("  " + files[j] + "\n")
		}
		if len(files) > FindPerDirMax {
			b.WriteString("  +" + itoa(len(files)-FindPerDirMax) + "\n")
		}
		b.WriteString("\n")
	}
	if len(dirs) > FindTotalDirMax {
		b.WriteString("+" + itoa(len(dirs)-FindTotalDirMax) + " more dirs\n")
	}

	return b.String()
}

// splitPath returns (parent, basename). For paths without a slash, parent
// is "." (matching Rust's PathBuf::from(...).parent().display() behaviour
// for relative basenames).
func splitPath(path string) (string, string) {
	idx := strings.LastIndexByte(path, '/')
	if idx == -1 {
		return ".", path
	}
	dir := path[:idx]
	if dir == "" {
		dir = "/"
	}
	return dir, path[idx+1:]
}
