#!/usr/bin/env python3
"""Run the real API/gateway suite and retain independently inspectable evidence."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile
import zipfile
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parent.parent
COMMAND = ["go", "-C", "backend", "test", "./cmd/api", "-run", "^TestOutboundE2E$", "-count=1", "-json"]


def sha256(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_identity():
    paths = subprocess.check_output(
        ["git", "ls-files", "-co", "--exclude-standard", "-z"], cwd=ROOT
    ).decode().split("\0")
    files = {}
    for name in sorted(set(paths) - {""}):
        path = ROOT / name
        if path.is_file():
            files[name] = sha256(path)
    return {
        "commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "files": files,
    }


def describe(command):
    result = subprocess.run(command, cwd=ROOT, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    return {"command": command, "exit_code": result.returncode, "output": result.stdout.strip()}


def main():
    artifact_root = ROOT / ".artifacts" / "api-e2e"
    artifact_root.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix=datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ-"), dir=artifact_root))
    result = {
        "schema_version": 1,
        "started_at": datetime.now(timezone.utc).isoformat(),
        "reproduce": "QWG_E2E_MYSQL_IMAGE=" + os.environ.get("QWG_E2E_MYSQL_IMAGE", "mysql:8.4") + " make api-e2e",
        "command": COMMAND,
        "environment": {"QWG_E2E": "1", "QWG_E2E_MYSQL_IMAGE": os.environ.get("QWG_E2E_MYSQL_IMAGE", "mysql:8.4")},
        "platform": platform.platform(),
        "status": "failed",
        "errors": [],
        "scenarios": {},
    }
    print(f"E2E evidence: {directory}", flush=True)
    try:
        before = source_identity()
        (directory / "source.json").write_text(json.dumps(before, indent=2) + "\n")
        (directory / "changes.patch").write_bytes(subprocess.check_output(
            ["git", "diff", "--binary", "HEAD"], cwd=ROOT
        ))
        untracked = subprocess.check_output(
            ["git", "ls-files", "--others", "--exclude-standard", "-z"], cwd=ROOT
        ).decode().split("\0")
        with zipfile.ZipFile(directory / "untracked.zip", "w", zipfile.ZIP_DEFLATED) as archive:
            for name in untracked:
                if name and (ROOT / name).is_file():
                    archive.write(ROOT / name, name)
        result["tools"] = {"go": describe(["go", "version"]), "docker": describe(["docker", "version", "--format", "{{json .}}"])}
        coverage = describe([sys.executable, "scripts/check-api-e2e-coverage.py"])
        (directory / "coverage-check.json").write_text(json.dumps(coverage, indent=2) + "\n")
        print(coverage["output"], flush=True)
        (directory / "coverage.json").write_bytes((ROOT / "backend/cmd/api/e2e-coverage.json").read_bytes())
        if coverage["exit_code"]:
            result["errors"].append("Operation coverage inventory failed")
        else:
            environment = dict(os.environ, QWG_E2E="1")
            package_passed = False
            with (directory / "events.jsonl").open("w") as events, (directory / "stderr.log").open("w") as stderr:
                process = subprocess.Popen(COMMAND, cwd=ROOT, env=environment, text=True, stdout=subprocess.PIPE, stderr=stderr)
                try:
                    for line in process.stdout:
                        events.write(line)
                        events.flush()
                        try:
                            event = json.loads(line)
                        except json.JSONDecodeError:
                            result["errors"].append("Invalid Go test JSON output")
                            continue
                        if event.get("Output"):
                            print(event["Output"], end="", flush=True)
                        action = event.get("Action")
                        test = event.get("Test")
                        if test and action in ("pass", "fail", "skip"):
                            result["scenarios"][test] = {"status": action, "elapsed_seconds": event.get("Elapsed")}
                        if action == "fail":
                            result["errors"].append(f"Go test failed: {test or event.get('Package')}")
                        if not test and action == "pass":
                            package_passed = True
                    result["exit_code"] = process.wait()
                finally:
                    if process.poll() is None:
                        process.terminate()
                        process.wait()
            if result["exit_code"] != 0:
                result["errors"].append(f"Go exited with {result['exit_code']}")
            if not package_passed or result["scenarios"].get("TestOutboundE2E", {}).get("status") != "pass":
                result["errors"].append("Required E2E suite did not pass")
            if any(value["status"] != "pass" for name, value in result["scenarios"].items() if name.startswith("TestOutboundE2E/")):
                result["errors"].append("An E2E scenario failed or was skipped")
            result["images"] = describe(["docker", "image", "inspect", "mysql:8.4", "redis:7-alpine", "--format", "{{json .RepoDigests}} {{.Id}}"])
        if source_identity() != before:
            result["errors"].append("Source changed during execution; rerun against stable source")
        if not result["errors"]:
            result["status"] = "passed"
    except (Exception, KeyboardInterrupt) as error:
        result["errors"].append(f"{type(error).__name__}: {error}")
    result["finished_at"] = datetime.now(timezone.utc).isoformat()
    (directory / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    checksums = {path.name: sha256(path) for path in sorted(directory.iterdir()) if path.is_file()}
    (directory / "checksums.json").write_text(json.dumps(checksums, indent=2) + "\n")
    print(f"E2E {result['status']}: {directory / 'result.json'}", flush=True)
    for error in result["errors"]:
        print(error, file=sys.stderr)
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
