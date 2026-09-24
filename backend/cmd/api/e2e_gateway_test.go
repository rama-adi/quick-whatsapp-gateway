package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/store"
)

type e2eGateway struct {
	bin         string
	cmd         *exec.Cmd
	done        chan error
	output      bytes.Buffer
	controlURL  string
	engineAddr  string
	journalPath string
	credentials string
	enrollment  e2eGatewayEnrollment
}

func (infra *e2eInfra) startGateway(t *testing.T) *e2eGateway {
	t.Helper()
	enrollment := createE2EGatewayEnrollment(t, infra.db)
	t.Log("gateway enrollment issued")
	seedE2ESession(t, infra.db, enrollment.id)
	t.Log("session and quote seeded")
	gateway := &e2eGateway{
		bin:         filepath.Join(t.TempDir(), "e2e-gateway.test"),
		controlURL:  "http://" + e2eFreeAddr(t),
		engineAddr:  e2eFreeAddr(t),
		journalPath: filepath.Join(t.TempDir(), "gateway-journal.sqlite"),
		credentials: filepath.Join(t.TempDir(), "gateway-identity"),
		enrollment:  enrollment,
	}
	build := exec.Command("go", "-C", filepath.Join(infra.root, "backend"),
		"test", "-c", "-o", gateway.bin, "./cmd/gateway")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gateway test process: %v\n%s", err, out)
	}
	t.Log("gateway test binary built")
	gateway.run(t, infra)
	t.Cleanup(func() { gateway.stop() })
	return gateway
}

func (gateway *e2eGateway) run(t *testing.T, infra *e2eInfra) {
	t.Helper()
	gateway.output.Reset()
	gateway.cmd = exec.Command(gateway.bin, "-test.run=^TestE2EGatewayProcess$", "-test.v")
	gateway.cmd.Dir = infra.root
	gateway.cmd.Env = append(os.Environ(),
		"QWG_E2E_GATEWAY=1",
		"QWG_E2E_API_GRPC="+infra.apiPrivate,
		"QWG_E2E_GATEWAY_ID="+gateway.enrollment.id,
		"QWG_E2E_ENROLLMENT_TOKEN="+gateway.enrollment.token,
		"QWG_E2E_BOOTSTRAP_CA="+gateway.enrollment.caPath,
		"QWG_E2E_CREDENTIAL_DIR="+gateway.credentials,
		"QWG_E2E_JOURNAL_PATH="+gateway.journalPath,
		"QWG_E2E_ENGINE_ADDR="+gateway.engineAddr,
		"QWG_E2E_CONTROL_ADDR="+gateway.controlURL[len("http://"):],
		"QWG_E2E_SESSION_ID="+e2eSessionID,
		"QWG_E2E_ORG_ID="+e2eOrgA,
		"QWG_E2E_DEVICE_JID="+e2eDeviceJID,
		"QWG_E2E_DEVICE_LID="+e2eDeviceLID,
	)
	gateway.cmd.Stdout = &gateway.output
	gateway.cmd.Stderr = &gateway.output
	if err := gateway.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	gateway.done = make(chan error, 1)
	go func() { gateway.done <- gateway.cmd.Wait() }()
	ctx, cancel := e2eContext(t)
	defer cancel()
	e2eEventually(t, ctx, "gateway desired-state readiness", func() bool {
		select {
		case err := <-gateway.done:
			gateway.cmd = nil
			t.Fatalf("gateway process exited before ready: %v\n%s", err, gateway.output.String())
		default:
		}
		res, err := http.Get(gateway.controlURL + "/health") //nolint:gosec -- loopback child process
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK
	})
	e2eEventually(t, ctx, "API engine target convergence", func() bool {
		target, err := store.NewGatewayRepo(infra.db).ResolveSessionEngineTarget(
			ctx, e2eOrgA, e2eSessionID)
		return err == nil && target.GatewayID == gateway.enrollment.id &&
			target.GRPCEndpoint == gateway.engineAddr
	})
}

func (gateway *e2eGateway) stop() {
	if gateway.cmd == nil || gateway.cmd.Process == nil {
		return
	}
	_ = gateway.cmd.Process.Signal(os.Interrupt)
	<-gateway.done
	gateway.cmd = nil
}

func (gateway *e2eGateway) getCaptures(t *testing.T) []e2eCapture {
	t.Helper()
	res, err := http.Get(gateway.controlURL + "/captures") //nolint:gosec -- loopback child process
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("gateway captures returned %d", res.StatusCode)
	}
	captures := []e2eCapture{}
	if err := json.NewDecoder(res.Body).Decode(&captures); err != nil {
		t.Fatal(err)
	}
	return captures
}

type e2eCapture struct {
	ID      string          `json:"id"`
	To      string          `json:"to"`
	Message json.RawMessage `json:"message"`
}

func (gateway *e2eGateway) fault(t *testing.T, mode string) {
	t.Helper()
	response, err := http.Post(
		gateway.controlURL+"/fault", "application/json",
		bytes.NewBufferString(fmt.Sprintf(`{"mode":%q}`, mode)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("set gateway fault %q: %d", mode, response.StatusCode)
	}
}

func (gateway *e2eGateway) waitCaptureCount(t *testing.T, count int) []e2eCapture {
	t.Helper()
	ctx, cancel := e2eContext(t)
	defer cancel()
	var captures []e2eCapture
	e2eEventually(t, ctx, "captured WhatsApp send", func() bool {
		captures = gateway.getCaptures(t)
		return len(captures) == count
	})
	return captures
}
