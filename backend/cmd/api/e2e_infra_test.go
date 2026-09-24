package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/dbmigrate"
)

// TestE2EAPIProcess executes the exact production composition root in a child
// test process. The parent owns the disposable dependencies and sends SIGTERM.
func TestE2EAPIProcess(t *testing.T) {
	if os.Getenv("QWG_E2E_API_PROCESS") != "1" {
		t.Skip("API process is started by TestOutboundE2E")
	}
	if err := runWithStorageTransport(e2eStorageTransport()); err != nil {
		t.Fatal(err)
	}
}

type e2eInfra struct {
	db                   *sql.DB
	dsn                  string
	redisURL             string
	apiURL               string
	apiGRPC              string
	apiPrivate           string
	apiHTTP              string
	apiIdentityDir       string
	apiCleanupRegistered bool
	authURL              string
	externalCA           string
	apiCmd               *exec.Cmd
	apiOutput            e2eSafeBuffer
	root                 string
}

type e2eSafeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *e2eSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *e2eSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func e2eStartInfra(t *testing.T) *e2eInfra {
	t.Helper()
	if os.Getenv("QWG_E2E") != "1" {
		t.Skip("set QWG_E2E=1 to run Docker-backed API/gateway end-to-end tests")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := e2eContext(t)
	t.Cleanup(cancel)
	name := fmt.Sprintf("qwg-e2e-%d", os.Getpid())
	mysqlID := e2eDockerRun(t, ctx, name+"-mysql", "mysql:8.4",
		"-e", "MYSQL_ROOT_PASSWORD=e2e-password",
		"-e", "MYSQL_DATABASE=qwg_e2e_test",
		"-p", "127.0.0.1::3306",
	)
	redisID := e2eDockerRun(t, ctx, name+"-redis", "redis:7-alpine", "-p", "127.0.0.1::6379")
	mysqlAddr := e2eDockerPort(t, ctx, mysqlID, "3306/tcp")
	redisAddr := e2eDockerPort(t, ctx, redisID, "6379/tcp")
	infra := &e2eInfra{
		dsn:      fmt.Sprintf("root:e2e-password@tcp(%s)/qwg_e2e_test?parseTime=true&multiStatements=true", mysqlAddr),
		redisURL: "redis://" + redisAddr,
		root:     root,
	}
	infra.db, err = sql.Open("mysql", infra.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = infra.db.Close() })
	e2eEventually(t, ctx, "MySQL ready", func() bool {
		return exec.CommandContext(ctx, "docker", "exec", mysqlID,
			"mysql", "--silent", "-uroot", "-pe2e-password",
			"-D", "qwg_e2e_test", "-e", "SELECT 1").Run() == nil
	})
	e2eEventually(t, ctx, "MySQL TCP ready", func() bool {
		return infra.db.PingContext(ctx) == nil
	})
	if err := dbmigrate.Run(infra.dsn, dbmigrate.Up); err != nil {
		t.Fatalf("migrate disposable MySQL: %v", err)
	}
	return infra
}

func e2eDockerRun(t *testing.T, ctx context.Context, name, image string, options ...string) string {
	t.Helper()
	args := append([]string{"run", "--detach", "--rm", "--name", name}, options...)
	args = append(args, image)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("start %s: %v\n%s", image, err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", id).Run()
	})
	return id
}

func e2eDockerPort(t *testing.T, ctx context.Context, id, port string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "port", id, port).CombinedOutput()
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func e2eContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		t.Fatal("E2E requires go test -timeout to bound external processes")
	}
	return context.WithDeadline(context.Background(), deadline)
}

func e2eEventually(t *testing.T, ctx context.Context, condition string, check func() bool) {
	t.Helper()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", condition, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func e2eFreeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func (infra *e2eInfra) startAPI(t *testing.T) {
	t.Helper()
	ctx, cancel := e2eContext(t)
	defer cancel()
	if infra.apiHTTP == "" {
		infra.apiHTTP = e2eFreeAddr(t)
		infra.apiGRPC = e2eFreeAddr(t)
		infra.apiPrivate = e2eFreeAddr(t)
		infra.apiIdentityDir = filepath.Join(t.TempDir(), "api-identity")
		infra.apiURL = "http://" + infra.apiHTTP
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	authURL := infra.authURL
	if authURL == "" {
		authURL = "http://127.0.0.1:1"
	}
	infra.apiCmd = exec.Command(os.Args[0], "-test.run=^TestE2EAPIProcess$", "-test.v")
	infra.apiCmd.Dir = infra.root
	infra.apiCmd.Env = append(os.Environ(),
		"QWG_E2E_API_PROCESS=1",
		"SSL_CERT_FILE="+infra.externalCA,
		"MYSQL_DSN="+infra.dsn,
		"REDIS_URL="+infra.redisURL,
		"API_HTTP_ADDR="+infra.apiHTTP,
		"API_PUBLIC_GRPC_ADDR="+infra.apiGRPC,
		"API_GATEWAY_GRPC_ADDR="+infra.apiPrivate,
		"API_GATEWAY_TLS_IDENTITY_DIR="+infra.apiIdentityDir,
		"API_GATEWAY_ENGINE_UNARY_DEADLINE=2s",
		"API_GATEWAY_ENGINE_SEND_DEADLINE=4m",
		"API_PUBLIC_URL="+infra.apiURL,
		"BETTER_AUTH_URL="+authURL,
		"BETTER_AUTH_JWKS_URL="+authURL+"/api/auth/jwks",
		"OIDC_KEY_ENC_KEY="+key,
		"OAUTH_CLIENT_SECRET_PEPPER=e2e-pepper",
		"APP_ENCRYPTION_KEY="+key,
		"PKI_ENCRYPTION_KEY="+key,
		"PKI_ENCRYPTION_KEY_ID=e2e",
		"WEB_LOGIN_URL=http://127.0.0.1:1/login/whatsapp",
	)
	infra.apiCmd.Stdout = &infra.apiOutput
	infra.apiCmd.Stderr = &infra.apiOutput
	if err := infra.apiCmd.Start(); err != nil {
		t.Fatal(err)
	}
	if !infra.apiCleanupRegistered {
		t.Cleanup(infra.stopAPI)
		infra.apiCleanupRegistered = true
	}
	e2eEventually(t, ctx, "API readiness", func() bool {
		res, err := http.Get(infra.apiURL + "/readyz") //nolint:gosec -- loopback test process
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK
	})
}

func (infra *e2eInfra) stopAPI() {
	if infra.apiCmd == nil || infra.apiCmd.Process == nil {
		return
	}
	_ = infra.apiCmd.Process.Signal(os.Interrupt)
	_ = infra.apiCmd.Wait()
	infra.apiCmd = nil
}
