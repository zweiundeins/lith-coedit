package collab_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The editor's fake writer (editor/test/authority/rebase.js) and the
// rebase prosemirror-go's oracle runs alike, comments and layout aside:
// the browser tests then test the rebase the Go writer runs.
func TestEditorRebaseIsTheOracles(t *testing.T) {
	dir, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/zweiundeins/prosemirror-go").Output()
	if err != nil {
		t.Skip("go list: ", err)
	}
	oracle, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(dir)), "testdata/oracle/src/rebase.js"))
	if err != nil {
		t.Skip("prosemirror-go has no oracle sources here: ", err)
	}
	ed, err := os.ReadFile("../editor/test/authority/rebase.js")
	if err != nil {
		t.Fatal(err)
	}
	code := func(b []byte) string {
		var out []string
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "//") {
				continue
			}
			out = append(out, strings.ReplaceAll(l, " ", ""))
		}
		return strings.Join(out, "\n")
	}
	if code(ed) != code(oracle) {
		t.Fatal("editor/test/authority/rebase.js differs from prosemirror-go's testdata/oracle/src/rebase.js")
	}
}
