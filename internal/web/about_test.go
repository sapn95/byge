package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The page renders, and it renders through the layout.
//
// This test exists because the route went in without one and answered 500:
// layout.html reads .BoardSlug off whatever it is given and aboutPage did not
// carry it. Every other check in the package passed, because nothing fetched
// the page. A registered route that nothing requests is a route nobody has
// run.
func TestAboutRenders(t *testing.T) {
	rec := newEnv(t, nil, nil).do(http.MethodGet, "/about", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The layout, so a failure here is not a bare fragment being served.
	for _, want := range []string{"<footer", `href="/about"`, "byge"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
}

// The attribution is read out of assets/VERSIONS and out of the binary, so
// what the page says and what the board ships cannot drift apart. These are
// the three facts somebody comes to this page for.
func TestAboutNamesWhatItShips(t *testing.T) {
	body := newEnv(t, nil, nil).do(http.MethodGet, "/about", nil).Body.String()

	// The fork and the licence.
	for _, want := range []string{
		"NicolasHaas/golang-kanban",
		"MIT",
		"LICENSE",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not mention %q", want)
		}
	}

	// Every vendored file, with the version and the licence that VERSIONS
	// records for it. An empty list would pass a test that only looked for the
	// heading, so this walks what the reader actually returns.
	got := vendoredCredits()
	if len(got) == 0 {
		t.Fatal("no vendored credits were read out of assets/VERSIONS")
	}
	for _, c := range got {
		for _, want := range []string{c.Name, c.Version, c.Licence, c.Source} {
			if want == "" {
				t.Errorf("credit %+v has an empty field", c)
				continue
			}
			if !strings.Contains(body, want) {
				t.Errorf("the page does not carry %q for %s", want, c.Name)
			}
		}
	}

	// The modules the binary was built from. Under `go test` the versions come
	// back as "(devel)" or empty for the main module's own dependencies in
	// some builds, so this asserts the paths rather than the numbers.
	for _, want := range []string{"github.com/lib/pq", "modernc.org/sqlite"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not name %s", want)
		}
	}
}

// The about page names the modules this project requires, and go.mod is where
// that requirement actually lives. A third dependency added there without a
// line on the page is a thing shipped without attribution, so this compares
// the two rather than trusting them to be edited together.
func TestDirectModulesMatchGoMod(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	// The first require block holds the direct requirements; the second is
	// marked // indirect on every line. Reading the markers rather than the
	// block order, because the order is gofmt's to change.
	var want []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.Contains(line, "// indirect") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 2 && strings.HasPrefix(f[1], "v") && strings.Contains(f[0], ".") {
			want = append(want, f[0])
		}
	}
	if len(want) == 0 {
		t.Fatal("no direct requirements were parsed out of go.mod")
	}
	sort.Strings(want)
	got := append([]string(nil), directModules...)
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("directModules = %v, go.mod requires %v", got, want)
	}
}
