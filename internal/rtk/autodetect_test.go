package rtk

import (
	"strings"
	"testing"
)

// autodetect_test.go verifies each filter trigger fires for its
// canonical input shape AND that ambiguous / structureless input falls
// through to the right fallback (dedup-log → smart-truncate → none).

func TestDetectGitDiff(t *testing.T) {
	input := "diff --git a/foo.go b/foo.go\n@@ -1,3 +1,4 @@\n-old\n+new\n"
	got := AutoDetect(input)
	if got.Name != FilterGitDiff {
		t.Fatalf("expected git-diff, got %q", got.Name)
	}
}

func TestDetectGitDiffHunkOnly(t *testing.T) {
	// Sometimes LLMs paste just a hunk, no diff header. Should still
	// catch via @@ marker.
	input := "@@ -10,5 +10,7 @@ func foo() {\n-bar\n+baz\n"
	got := AutoDetect(input)
	if got.Name != FilterGitDiff {
		t.Fatalf("expected git-diff (hunk-only), got %q", got.Name)
	}
}

func TestDetectGitStatusLongForm(t *testing.T) {
	input := "On branch main\nChanges not staged for commit:\n  modified:   foo.go\n"
	got := AutoDetect(input)
	if got.Name != FilterGitStatus {
		t.Fatalf("expected git-status, got %q", got.Name)
	}
}

func TestDetectGitStatusPorcelain(t *testing.T) {
	// Porcelain WITHOUT long-form header: must trip the >=60% ratio rule.
	input := strings.Repeat(" M src/a.go\n", 5) + strings.Repeat("?? src/b.go\n", 5)
	got := AutoDetect(input)
	if got.Name != FilterGitStatus {
		t.Fatalf("expected git-status (porcelain), got %q", got.Name)
	}
}

func TestDetectBuildOutputBeforePorcelain(t *testing.T) {
	// CRITICAL: cargo "Compiling foo" must not be misread as porcelain
	// status (since "Co" matches the porcelain prefix regex).
	input := strings.Repeat("   Compiling foo v0.1.0\n", 6)
	got := AutoDetect(input)
	if got.Name != FilterBuildOutput {
		t.Fatalf("expected build-output (cargo), got %q", got.Name)
	}
}

func TestDetectGrep(t *testing.T) {
	input := "src/foo.go:42:func Foo() error {\nsrc/bar.go:13:return errors.New\n"
	got := AutoDetect(input)
	if got.Name != FilterGrep {
		t.Fatalf("expected grep, got %q", got.Name)
	}
}

func TestDetectFind(t *testing.T) {
	// All path-like, no colons, ≥3 lines.
	input := "./src/a.go\n./src/b.go\n./internal/c.go\n./internal/d.go\n"
	got := AutoDetect(input)
	if got.Name != FilterFind {
		t.Fatalf("expected find, got %q", got.Name)
	}
}

func TestDetectTree(t *testing.T) {
	input := ".\n├── foo\n│   └── bar.go\n└── baz.go\n"
	got := AutoDetect(input)
	if got.Name != FilterTree {
		t.Fatalf("expected tree, got %q", got.Name)
	}
}

func TestDetectLs(t *testing.T) {
	input := "total 12\n-rw-r--r-- 1 user staff 4096 Jan 12 10:30 foo.go\n-rw-r--r-- 1 user staff 4096 Jan 12 10:30 bar.go\n-rw-r--r-- 1 user staff 4096 Jan 12 10:30 baz.go\n"
	got := AutoDetect(input)
	if got.Name != FilterLs {
		t.Fatalf("expected ls, got %q", got.Name)
	}
}

func TestDetectSearchList(t *testing.T) {
	input := "Result of search in '/path' (total 5 files):\n- src/foo.go\n- src/bar.go\n"
	got := AutoDetect(input)
	if got.Name != FilterSearchList {
		t.Fatalf("expected search-list, got %q", got.Name)
	}
}

func TestDetectReadNumbered(t *testing.T) {
	// Build 300 numbered lines so total >= SmartTruncateMinLines (250)
	// AND read-numbered hit ratio >= 70%.
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		b.WriteString("  ")
		b.WriteString(itoa(i))
		b.WriteString("|content for line ")
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	got := AutoDetect(b.String())
	if got.Name != FilterReadNumbered {
		t.Fatalf("expected read-numbered, got %q", got.Name)
	}
}

func TestDetectDedupLogFallback(t *testing.T) {
	// 5+ non-empty lines with no structural pattern → dedup-log
	input := "Some random log line\nAnother log line\nWith different content\nAnd another one\nFifth line here\n"
	got := AutoDetect(input)
	if got.Name != FilterDedupLog {
		t.Fatalf("expected dedup-log fallback, got %q", got.Name)
	}
}

func TestDetectSmartTruncate(t *testing.T) {
	// >= SmartTruncateMinLines (250) of structureless content.
	// dedup-log will catch it first since it has 5+ non-empty lines,
	// so smart-truncate only triggers for content that escapes
	// dedup-log too. We craft a single very long structureless line
	// + many trailing empty lines so non-empty count stays low.
	var b strings.Builder
	b.WriteString("opaque blob ")
	b.WriteString(strings.Repeat("x", 5000))
	b.WriteByte('\n')
	for i := 0; i < 300; i++ {
		b.WriteByte('\n')
	}
	got := AutoDetect(b.String())
	if got.Name != FilterSmartTruncate {
		t.Fatalf("expected smart-truncate, got %q", got.Name)
	}
}

func TestDetectNoMatch(t *testing.T) {
	// Tiny structureless input — no filter should fire.
	input := "just some text"
	got := AutoDetect(input)
	if got.Fn != nil {
		t.Fatalf("expected no filter, got %q", got.Name)
	}
}

func TestDetectEmptyInput(t *testing.T) {
	got := AutoDetect("")
	if got.Fn != nil {
		t.Fatalf("expected no filter on empty input, got %q", got.Name)
	}
}
