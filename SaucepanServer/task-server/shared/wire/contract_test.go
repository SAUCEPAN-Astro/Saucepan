package wire_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestNoHardcodedTopicLiterals is #451's guard: MQTT topic strings are defined
// once, in this package (topics.go), and every other file derives its subscribe
// filter / prefix from those constants via SubscribeFilter / TopicPrefix. A
// string literal that looks like a raw topic anywhere else in the module means
// the contract has been duplicated again and a rename here would silently stop
// matching it.
//
// Scope: non-test .go files outside shared/wire/. Test files may build topics
// from literals as fixtures — that is not contract duplication.
func TestNoHardcodedTopicLiterals(t *testing.T) {
	moduleRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// A leading topic segment we own: "/telemetry/...", "/status/...", a bare
	// "/board/..." etc. The trailing char keeps "/status" (a substring of an
	// unrelated identifier) from matching while catching "/status/" and
	// "/status/%s".
	topicLit := regexp.MustCompile(`^/(telemetry|metadata|status|commands|board)(/|$)`)

	var offenders []string
	err = filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "shared" && filepath.Dir(path) == moduleRoot {
				// don't descend shared/wire; the rest of shared/ is fair game
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(moduleRoot, path)
		if strings.HasPrefix(rel, filepath.Join("shared", "wire")+string(filepath.Separator)) {
			return nil
		}

		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // not our problem to report here
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if topicLit.MatchString(s) {
				offenders = append(offenders, rel+": "+lit.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, o := range offenders {
		t.Errorf("hardcoded MQTT topic literal — use wire.SubscribeFilter / wire.TopicPrefix on a Topic* const instead:\n\t%s", o)
	}
}
