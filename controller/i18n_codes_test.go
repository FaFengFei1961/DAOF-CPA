package controller

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var backendMessageCodePattern = regexp.MustCompile(`^(ERR|SUCCESS)_[A-Z0-9_]+$`)

func TestBackendMessageCodesCoveredByLocales(t *testing.T) {
	root := projectRootForTest(t)
	codes := collectBackendMessageCodes(t, root)
	if len(codes) == 0 {
		t.Fatal("no backend message codes found")
	}

	for _, lang := range []string{"zh-CN", "en-US"} {
		api := readLocaleAPIKeys(t, filepath.Join(root, "i18n", lang+".json"))
		var missing []string
		for code := range codes {
			if _, ok := api[code]; !ok {
				missing = append(missing, code)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Fatalf("%s API translations missing %d backend message codes:\n%s", lang, len(missing), strings.Join(missing, "\n"))
		}
	}
}

func projectRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if filepath.Base(wd) == "controller" {
		return filepath.Dir(wd)
	}
	return wd
}

func collectBackendMessageCodes(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	files := []string{filepath.Join(root, "main.go")}
	for _, dir := range []string{"controller", "proxy", "middleware"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	codes := make(map[string]struct{})
	fset := token.NewFileSet()
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if backendMessageCodePattern.MatchString(value) {
				codes[value] = struct{}{}
			}
			return true
		})
	}
	return codes
}

func readLocaleAPIKeys(t *testing.T, file string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var locale struct {
		API map[string]string `json:"API"`
	}
	if err := json.Unmarshal(raw, &locale); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	if locale.API == nil {
		t.Fatalf("%s has no API namespace", file)
	}
	return locale.API
}
