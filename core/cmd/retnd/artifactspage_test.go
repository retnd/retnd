package main

import (
	"strings"
	"testing"
)

func TestPageWindow_1180(t *testing.T) {
	rows := make([]int, 34)
	for i := range rows {
		rows[i] = i
	}
	cases := []struct {
		name          string
		limit, offset int
		first, n      int
		describe      string
	}{
		{"page one", 20, 0, 0, 20, "showing 1-20 of 34 (--offset 20 for the next page)"},
		{"page two is short and ends the paging", 20, 20, 20, 14, "showing 21-34 of 34"},
		{"offset past the end", 20, 40, 0, 0, "showing 0 of 34 (--offset 40 is past the end)"},
		{"offset alone runs to the end", 0, 30, 30, 4, "showing 31-34 of 34"},
	}
	for _, c := range cases {
		w, ok := parsePageFlags(c.limit, c.offset)
		if !ok || !w.set() {
			t.Fatalf("%s: window %+v ok=%v", c.name, w, ok)
		}
		got := windowOf(w, rows)
		if len(got) != c.n || (c.n > 0 && got[0] != c.first) {
			t.Errorf("%s: got %d rows from %v, want %d from %d", c.name, len(got), got, c.n, c.first)
		}
		if d := w.describe(len(got), len(rows)); d != c.describe {
			t.Errorf("%s: describe = %q, want %q", c.name, d, c.describe)
		}
	}
	if w, _ := parsePageFlags(0, 0); w.set() || len(windowOf(w, rows)) != 34 {
		t.Error("no flags must leave the list whole and print nothing extra")
	}
	if _, ok := parsePageFlags(-1, 0); ok {
		t.Error("a negative limit must be refused")
	}
	if _, ok := parsePageFlags(0, -1); ok {
		t.Error("a negative offset must be refused")
	}
}

func TestArtifacts_Limit_1180(t *testing.T) {
	configPath := scrubbedRottedCopy(t)

	t.Run("a page that holds the list prints it and says which rows", func(t *testing.T) {
		out := artifactsOut(t, 0, "artifacts", "--config", configPath, "--limit", "20")
		if !strings.Contains(out, "1 artifact(s)") || !strings.Contains(out, "showing 1-1 of 1\n") {
			t.Errorf("unexpected page output:\n%s", out)
		}
	})
	t.Run("an offset past the end prints no rows and says so", func(t *testing.T) {
		out := artifactsOut(t, 0, "artifacts", "--config", configPath, "--limit", "20", "--offset", "5")
		if !strings.Contains(out, "0 artifact(s)") || !strings.Contains(out, "--offset 5 is past the end") {
			t.Errorf("unexpected page output:\n%s", out)
		}
	})
	t.Run("without the flags nothing extra is printed", func(t *testing.T) {
		if out := artifactsOut(t, 0, "artifacts", "--config", configPath); strings.Contains(out, "showing") {
			t.Errorf("the plain list grew a paging line:\n%s", out)
		}
	})
	t.Run("a negative number and an artifact operand are usage errors", func(t *testing.T) {
		artifactsOut(t, 2, "artifacts", "--config", configPath, "--limit", "-1")
		artifactsOut(t, 2, "artifacts", "--config", configPath, "--offset", "-1")
		artifactsOut(t, 2, "artifacts", "--config", configPath, "--limit", "5", assuranceID)
	})
}
