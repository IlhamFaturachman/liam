package rtk

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Ls compacts `ls -la` output. Output shape:
//
//	dir1/
//	dir2/
//	file.go  4.2K
//	other.md  120B
//
//	Summary: 12 files, 3 dirs (8 .go, 3 .md, 1 .json)
//
// Rationale: LLMs care about presence + size hints, not the perm/owner
// columns. The summary line gives a fast at-a-glance impression of file
// type distribution which is enough for "what's in this directory" reads.
func Ls(input string) string {
	var dirs []string
	type fileEntry struct {
		name string
		size string
	}
	var files []fileEntry
	byExt := map[string]int{}

	for _, line := range strings.Split(input, "\n") {
		if strings.HasPrefix(line, "total ") || line == "" {
			continue
		}
		parsed := parseLsLine(line)
		if parsed == nil {
			continue
		}
		if parsed.name == "." || parsed.name == ".." {
			continue
		}
		// Hide build/vendor noise — almost never the answer to
		// "what's in this folder".
		if isLsNoise(parsed.name) {
			continue
		}
		switch parsed.fileType {
		case 'd':
			dirs = append(dirs, parsed.name)
		case '-', 'l':
			ext := "no ext"
			if dot := strings.LastIndexByte(parsed.name, '.'); dot > 0 {
				ext = parsed.name[dot:]
			}
			byExt[ext]++
			files = append(files, fileEntry{parsed.name, humanSize(parsed.size)})
		}
	}

	if len(dirs) == 0 && len(files) == 0 {
		return input
	}

	var b strings.Builder
	for _, d := range dirs {
		b.WriteString(d + "/\n")
	}
	for _, f := range files {
		b.WriteString(f.name + "  " + f.size + "\n")
	}

	b.WriteString("\nSummary: " + itoa(len(files)) + " files, " + itoa(len(dirs)) + " dirs")
	if len(byExt) > 0 {
		type kv struct {
			ext string
			n   int
		}
		ext := make([]kv, 0, len(byExt))
		for k, v := range byExt {
			ext = append(ext, kv{k, v})
		}
		sort.Slice(ext, func(i, j int) bool {
			if ext[i].n != ext[j].n {
				return ext[i].n > ext[j].n
			}
			return ext[i].ext < ext[j].ext
		})
		b.WriteString(" (")
		limit := len(ext)
		if limit > LsExtSummaryTop {
			limit = LsExtSummaryTop
		}
		parts := make([]string, 0, limit)
		for i := 0; i < limit; i++ {
			parts = append(parts, itoa(ext[i].n)+" "+ext[i].ext)
		}
		b.WriteString(strings.Join(parts, ", "))
		if len(ext) > LsExtSummaryTop {
			b.WriteString(", +" + itoa(len(ext)-LsExtSummaryTop) + " more")
		}
		b.WriteString(")")
	}

	return b.String()
}

type lsParsed struct {
	fileType byte
	size     int64
	name     string
}

// parseLsLine pulls (perm[0], size, name) out of a `ls -la` line. The date
// regex anchors parsing — everything before is metadata, everything after
// is the filename.
func parseLsLine(line string) *lsParsed {
	loc := lsDateRe.FindStringIndex(line)
	if loc == nil {
		return nil
	}
	name := line[loc[1]:]
	beforeDate := line[:loc[0]]
	parts := strings.Fields(beforeDate)
	if len(parts) < 4 {
		return nil
	}
	perms := parts[0]
	if perms == "" {
		return nil
	}
	// Rightmost integer before the date is the size.
	var size int64
	for i := len(parts) - 1; i >= 0; i-- {
		n, err := strconv.ParseInt(parts[i], 10, 64)
		if err == nil {
			size = n
			break
		}
	}
	return &lsParsed{fileType: perms[0], size: size, name: name}
}

func humanSize(bytes int64) string {
	if bytes >= 1_048_576 {
		return strconv.FormatFloat(float64(bytes)/1_048_576, 'f', 1, 64) + "M"
	}
	if bytes >= 1024 {
		return strconv.FormatFloat(float64(bytes)/1024, 'f', 1, 64) + "K"
	}
	return strconv.FormatInt(bytes, 10) + "B"
}

func isLsNoise(name string) bool {
	for _, n := range LsNoiseDirs {
		if n == name {
			return true
		}
	}
	return false
}

// lsDateRe matches `ls -la` date column ("Jan 12 2025" or "Jan 12 14:30").
// Parser uses .FindStringIndex so we can split line into "before-date" and
// "after-date" cleanly; the matched slice (the date itself) is discarded.
var lsDateRe = regexp.MustCompile(`\s+(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+(\d{4}|\d{2}:\d{2})\s+`)
