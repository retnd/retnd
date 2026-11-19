package assurancegate_test

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The site-wide dead-fragment check and the docs link check (#988's release
// gate). A link that points at a page or at an id that is not there is the
// quiet kind of breakage a documentation change causes: nothing fails, the
// reader lands at the top of the wrong page. This epic rewrote or added a
// large share of the site and of docs/, so the check is over all of it and
// not over the pages this epic touched.
//
// It is a function over a file reader, so the same code runs against
// deliberately broken pages (TestTheLinkCheckCanFail), which is the mutation
// test a new guard owes.

var (
	htmlID   = regexp.MustCompile(`\b(?:id|name)="([^"]+)"`)
	htmlRef  = regexp.MustCompile(`\b(?:href|src)="([^"]*)"`)
	mdLink   = regexp.MustCompile(`\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	external = regexp.MustCompile(`^(?:[a-z][a-z0-9+.-]*:|//)`)
)

// linkProblems returns one finding per dead link in the given documents.
// files maps a repository-relative path to its text; only .html and .md
// entries are scanned, and every target is resolved against this same map.
func linkProblems(files map[string]string) []string {
	ids := map[string]map[string]bool{}
	for p, text := range files {
		if strings.HasSuffix(p, ".html") {
			set := map[string]bool{}
			for _, m := range htmlID.FindAllStringSubmatch(text, -1) {
				set[m[1]] = true
			}
			ids[p] = set
		}
	}

	var out []string
	check := func(from, target string, fragmentsOnly bool) {
		if target == "" || external.MatchString(target) {
			return
		}
		file, frag, _ := strings.Cut(target, "#")
		if i := strings.IndexByte(file, '?'); i >= 0 {
			file = file[:i]
		}
		resolved := from
		if file != "" {
			resolved = path.Clean(path.Join(path.Dir(from), file))
			if strings.HasPrefix(file, "/") {
				return // absolute site paths are the host's, not this tree's
			}
			if _, ok := files[resolved]; !ok {
				if !fileExistsOnDisk(resolved) {
					out = append(out, from+" links to "+target+", which is not in the tree")
					return
				}
			}
		}
		if frag == "" || fragmentsOnly {
			return
		}
		if set, isHTML := ids[resolved]; isHTML && !set[frag] {
			out = append(out, from+" links to "+target+", but "+resolved+" has no id \""+frag+"\"")
		}
	}

	for p, text := range files {
		switch {
		case strings.HasSuffix(p, ".html"):
			for _, m := range htmlRef.FindAllStringSubmatch(text, -1) {
				check(p, m[1], false)
			}
		case strings.HasSuffix(p, ".md"):
			for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
				// A Markdown heading's slug is the renderer's business and
				// differs between them, so a fragment into a .md file is not
				// judged; a fragment into an .html page is, because ids are
				// literal there.
				check(p, m[1], strings.Contains(m[1], ".md#") || strings.HasPrefix(m[1], "#"))
			}
		}
	}
	sort.Strings(out)

	return out
}

// fileExistsOnDisk lets a link point at a file that is not a document (an
// image, a script) without the map having to hold every byte of the repo.
var diskRoot string

func fileExistsOnDisk(rel string) bool {
	if diskRoot == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(diskRoot, filepath.FromSlash(rel)))

	return err == nil
}

func TestEveryLinkInTheSiteAndTheDocsResolves(t *testing.T) {
	root := repoRoot(t)
	diskRoot = root
	t.Cleanup(func() { diskRoot = "" })

	files := map[string]string{}
	for _, top := range []string{"docs", "README.md"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return fs.SkipDir
				}

				return nil
			}
			if strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".md") {
				b, rerr := os.ReadFile(p)
				if rerr == nil {
					rel, _ := filepath.Rel(root, p)
					files[filepath.ToSlash(rel)] = string(b)
				}
			}

			return nil
		})
	}
	if b, err := os.ReadFile(filepath.Join(root, "README.md")); err == nil {
		files["README.md"] = string(b)
	}
	if len(files) < 40 {
		t.Fatalf("found %d documents; the walk is not seeing the docs tree", len(files))
	}
	if _, ok := files["docs/site/reference.html"]; !ok {
		t.Fatal("the site's reference page was not scanned, so this check would pass over the site it exists for")
	}

	// A positive control on the input: a scan that found no links would pass
	// by having nothing to look at.
	links := 0
	for p, text := range files {
		if strings.HasSuffix(p, ".html") {
			links += len(htmlRef.FindAllString(text, -1))
		} else {
			links += len(mdLink.FindAllString(text, -1))
		}
	}
	if links < 200 {
		t.Fatalf("found only %d links across %d documents; the patterns have stopped matching", links, len(files))
	}

	for _, p := range linkProblems(files) {
		t.Error(p)
	}
}

func TestTheLinkCheckCanFail(t *testing.T) {
	good := map[string]string{
		"docs/site/a.html": `<a id="top"></a><a href="#top">up</a><a href="b.html#deep">there</a><a href="https://example.com/x">out</a>`,
		"docs/site/b.html": `<h2 id="deep">x</h2>`,
		"docs/x.md":        "see [the page](site/b.html#deep) and [the section](#anything) and [more](y.md#slug)",
		"docs/y.md":        "# y",
	}
	if got := linkProblems(good); len(got) != 0 {
		t.Fatalf("a page with only good links was refused: %v", got)
	}

	cases := []struct {
		name   string
		mutate func(m map[string]string)
		want   string
	}{
		{"a fragment on the same page with no id", func(m map[string]string) {
			m["docs/site/a.html"] += `<a href="#nowhere">x</a>`
		}, `has no id "nowhere"`},
		{"a fragment into another page with no id", func(m map[string]string) {
			m["docs/site/a.html"] += `<a href="b.html#gone">x</a>`
		}, `has no id "gone"`},
		{"a link to a page that is not there", func(m map[string]string) {
			m["docs/site/a.html"] += `<a href="missing.html">x</a>`
		}, "which is not in the tree"},
		{"a Markdown link to a missing document", func(m map[string]string) {
			m["docs/x.md"] += " [dead](nope.md)"
		}, "which is not in the tree"},
		{"a Markdown link into an HTML page's missing id", func(m map[string]string) {
			m["docs/x.md"] += " [dead](site/b.html#absent)"
		}, `has no id "absent"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range good {
				m[k] = v
			}
			c.mutate(m)
			got := strings.Join(linkProblems(m), "\n")
			if !strings.Contains(got, c.want) {
				t.Fatalf("the check did not refuse %q: wanted %q, got %q", c.name, c.want, got)
			}
		})
	}
}
