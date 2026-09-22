package application_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestApplicationBoundaryHasNoTransportOrImplementationImports(t *testing.T) {
	forbidden := []string{
		"github.com/danielgtaylor/huma",
		"/gen/",
		"internal/store",
		"internal/wa",
		"internal/outbound",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range parsed.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, denied := range forbidden {
				if strings.Contains(path, denied) {
					t.Fatalf("%s imports forbidden application dependency %q", file, path)
				}
			}
		}
	}
}
