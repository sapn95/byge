package web

import (
	"bufio"
	"io/fs"
	"runtime/debug"
	"strings"
	"sync"

	"byge/assets"
)

// credit is one thing this board ships that somebody else wrote.
type credit struct {
	Name    string
	Version string
	Licence string
	Source  string
}

// vendoredCredits are the front-end files copied into assets/vendor, read out
// of assets/VERSIONS rather than written down again here.
//
// That file is already the record: it carries the name, the version, the
// licence and where each file came from, and the update procedure is to
// replace the file and its line together. A second copy of those facts on the
// about page is a second copy to go stale, and the one that goes stale is the
// one nobody updates because nobody remembers it is there.
var vendoredCredits = sync.OnceValue(func() []credit {
	b, err := fs.ReadFile(assets.FS(), "VERSIONS")
	if err != nil {
		return nil
	}
	var out []credit
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Comments, the sha256 block and the blank lines between them. A
		// credit line is four fields; everything else in that file is either
		// prose or a digest and a path.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 4 || !strings.HasPrefix(f[3], "http") {
			continue
		}
		out = append(out, credit{Name: f[0], Version: f[1], Licence: f[2], Source: f[3]})
	}
	return out
})

// directModules are the two modules this project requires. They are named
// here because the about page has to list them whether or not the running
// binary can say anything about them, and because a list in one place that a
// test compares against go.mod cannot drift: TestDirectModulesMatchGoMod
// fails the moment a third requirement is added without this being touched.
var directModules = []string{"github.com/lib/pq", "modernc.org/sqlite"}

// goCredits are those modules with the version linked into this binary.
//
// debug.ReadBuildInfo answers with what is actually linked in, so a version
// shown here cannot disagree with the build. It answers with nothing at all
// under `go test`, and the page is drawn either way: a module named without a
// version still says whose work is in here, which is what the page is for.
//
// It carries no licence, which is why each row links to the module's page on
// pkg.go.dev and the page says that is where the licence is. Naming a licence
// this has not read would be making one up.
var goCredits = sync.OnceValue(func() []credit {
	versions := map[string]string{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, d := range info.Deps {
			if d != nil {
				versions[d.Path] = strings.TrimPrefix(d.Version, "v")
			}
		}
	}
	out := make([]credit, 0, len(directModules))
	for _, path := range directModules {
		out = append(out, credit{
			Name:    path,
			Version: versions[path],
			Source:  "https://pkg.go.dev/" + path + "?tab=licenses",
		})
	}
	return out
})
