// Package archtest enforces the architecture rules in CLAUDE.md as tests, so breaking
// one fails the build instead of relying on review:
//
//  1. Only the model gateway may reference an LLM provider's API host.
//  2. Only the model gateway may import an LLM vendor SDK.
//  3. Only internal/dataforseo may reference DataForSEO's API host.
//  4. The api role (internal/api) must not depend, even transitively, on any client
//     that calls an external service: pages read stored results, they never wait on one.
//  5. No SQL string may use SELECT * (prompts and payloads are built from explicit fields).
package archtest

import (
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

const module = "github.com/UncleSon21/vellatry"

var llmHosts = []string{
	"api.anthropic.com",
	"api.openai.com",
	"generativelanguage.googleapis.com",
	"aiplatform.googleapis.com",
	"api.perplexity.ai",
	"api.mistral.ai",
	"api.groq.com",
	"openrouter.ai",
}

var llmSDKs = []string{
	"github.com/anthropics/anthropic-sdk-go",
	"github.com/openai/openai-go",
	"github.com/sashabaranov/go-openai",
	"github.com/google/generative-ai-go",
	"google.golang.org/genai",
	"github.com/tmc/langchaingo",
}

// externalClients are packages that call services outside Vellatry. The api role may
// not depend on any of them. (internal/googleauth and internal/asanaauth only build
// URLs and are allowed.)
var externalClients = []string{
	module + "/internal/dataforseo",
	module + "/internal/platform/gateway",
	module + "/internal/google",
	module + "/internal/warehouse",
	module + "/internal/site", // the crawler fetches customers' sites
	module + "/internal/slack",
	module + "/internal/email",
	module + "/internal/asana",
	module + "/internal/pdf",
	module + "/internal/embed", // calls ml/embed over the private network
	module + "/internal/workers",
}

var selectStar = regexp.MustCompile(`(?i)\bselect\s+\*\s+from\b`)

type goFile struct {
	path    string // repo-relative, slash-separated
	pkg     string // import path
	test    bool
	imports []string
	strs    []string
}

func TestLLMHostsOnlyInGateway(t *testing.T) {
	for _, f := range files(t) {
		if f.test || underDir(f.pkg, module+"/internal/platform/gateway") {
			continue
		}
		for _, s := range f.strs {
			for _, h := range llmHosts {
				if strings.Contains(s, h) {
					t.Errorf("%s references LLM host %q; only internal/platform/gateway may call an LLM", f.path, h)
				}
			}
		}
	}
}

func TestLLMSDKsOnlyInGateway(t *testing.T) {
	for _, f := range files(t) {
		if underDir(f.pkg, module+"/internal/platform/gateway") {
			continue // providers wrap vendor SDKs here and nowhere else
		}
		for _, imp := range f.imports {
			for _, sdk := range llmSDKs {
				if imp == sdk || strings.HasPrefix(imp, sdk+"/") {
					t.Errorf("%s imports LLM SDK %q; call LLMs through internal/platform/gateway", f.path, imp)
				}
			}
		}
	}
}

func TestDataForSEOHostOnlyInItsClient(t *testing.T) {
	for _, f := range files(t) {
		if f.test || underDir(f.pkg, module+"/internal/dataforseo") {
			continue
		}
		for _, s := range f.strs {
			if strings.Contains(s, "api.dataforseo.com") {
				t.Errorf("%s references DataForSEO's host; use internal/dataforseo", f.path)
			}
		}
	}
}

func TestAPIRoleNeverDependsOnExternalClients(t *testing.T) {
	graph := map[string][]string{}
	for _, f := range files(t) {
		if !f.test {
			graph[f.pkg] = append(graph[f.pkg], f.imports...)
		}
	}
	for pkg := range graph {
		if !underDir(pkg, module+"/internal/api") {
			continue
		}
		for _, dep := range closure(graph, pkg) {
			for _, ext := range externalClients {
				if underDir(dep, ext) {
					t.Errorf("%s depends on %s; the api role must only read stored results and enqueue work", pkg, dep)
				}
			}
		}
	}
}

func TestNoSelectStar(t *testing.T) {
	for _, f := range files(t) {
		for _, s := range f.strs {
			if selectStar.MatchString(s) {
				t.Errorf("%s contains SELECT *; name the columns", f.path)
			}
		}
	}
}

// TestScannerSeesTheRepo guards against the rules above passing vacuously.
func TestScannerSeesTheRepo(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range files(t) {
		seen[f.pkg] = true
	}
	for _, want := range []string{module + "/internal/platform/gateway", module + "/internal/dataforseo"} {
		if !seen[want] {
			t.Fatalf("scanner did not find %s; the architecture rules would pass vacuously", want)
		}
	}
}

var (
	cached []goFile
)

func files(t *testing.T) []goFile {
	t.Helper()
	if cached != nil {
		return cached
	}
	root := repoRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "node_modules", "vendor", "results":
				return filepath.SkipDir
			}
			if path != root && filepath.Base(filepath.Dir(path)) == "internal" && d.Name() == "archtest" {
				return filepath.SkipDir // this package lists the forbidden strings itself
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		af, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		f := goFile{
			path: rel,
			pkg:  strings.TrimSuffix(module+"/"+filepath.ToSlash(filepath.Dir(rel)), "/."),
			test: strings.HasSuffix(path, "_test.go"),
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			f.imports = append(f.imports, p)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					f.strs = append(f.strs, s)
				}
			}
			return true
		})
		cached = append(cached, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return cached
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func closure(graph map[string][]string, start string) []string {
	seen := map[string]bool{}
	var walk func(string)
	walk = func(p string) {
		for _, dep := range graph[p] {
			if !seen[dep] && strings.HasPrefix(dep, module+"/") {
				seen[dep] = true
				walk(dep)
			}
		}
	}
	walk(start)
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func underDir(pkg, dir string) bool { return pkg == dir || strings.HasPrefix(pkg, dir+"/") }
