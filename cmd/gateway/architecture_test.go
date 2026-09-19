package main

import (
	"context"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func forbiddenGatewayImport(path string) bool {
	return path == "github.com/ramaadi/quick-whatsapp-gateway/internal/service" ||
		path == "github.com/ramaadi/quick-whatsapp-gateway/internal/store" ||
		strings.HasPrefix(path, "github.com/ramaadi/quick-whatsapp-gateway/internal/store/") ||
		strings.HasPrefix(path, "github.com/redis/go-redis/") ||
		path == "github.com/go-sql-driver/mysql" ||
		path == "github.com/ramaadi/quick-whatsapp-gateway/internal/dbmigrate" ||
		path == "github.com/ramaadi/quick-whatsapp-gateway/migrations" ||
		strings.HasPrefix(path, "github.com/golang-migrate/migrate")
}

func TestGatewayPackageHasNoAPIPersistenceDependencies(t *testing.T) {
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
			if forbiddenGatewayImport(path) {
				t.Fatalf("%s directly imports forbidden API/persistence package %q", file, path)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list gateway dependencies: %v", err)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if forbiddenGatewayImport(dependency) {
			t.Fatalf("gateway transitively depends on forbidden API/persistence package %q", dependency)
		}
	}
}
