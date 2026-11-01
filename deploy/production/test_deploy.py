"""Exercise Git checkout and release transitions without SSH or a Docker daemon."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent
REAL_GIT = shutil.which("git")


class DeployTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="deploy-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.repo = self.root / "repository"
        self.repo.mkdir()
        self.git("init", "-q", str(self.repo))
        self.git("-C", str(self.repo), "config", "user.email", "test@example.invalid")
        self.git("-C", str(self.repo), "config", "user.name", "Deploy test")
        scripts = self.repo / "deploy/production"
        scripts.mkdir(parents=True)
        shutil.copyfile(SCRIPTS / "deploy.sh", scripts / "deploy.sh")
        (scripts / "Dockerfile.cached").write_text("FROM scratch\n")
        (self.repo / ".gitignore").write_text("ignored-local\n")
        self.git("-C", str(self.repo), "add", ".")
        self.git("-C", str(self.repo), "commit", "-qm", "baseline")
        self.deploy = self.root / "production"
        self.deploy.mkdir()
        self.source = self.deploy / "new-api"
        self.git("clone", "-q", str(self.repo), str(self.source))
        self.baseline = self.git("-C", str(self.source), "rev-parse", "HEAD").strip()
        (self.repo / "release.txt").write_text("requested version\n")
        self.git("-C", str(self.repo), "add", ".")
        self.git("-C", str(self.repo), "commit", "-qm", "release")
        self.revision = self.git("-C", str(self.repo), "rev-parse", "HEAD").strip()
        for name in ("compose.yaml", ".env", "runtime.env", "redis.conf"):
            (self.deploy / name).write_text("original\n")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.events = self.root / "events.jsonl"
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        REAL_GIT=REAL_GIT, DEPLOY_TEST_EVENTS=str(self.events))
        self.write_tool("ssh", '#!/bin/bash\nexec bash -c "$2"\n')
        self.write_tool("git", """#!/usr/bin/env python3
import os, sys
if sys.argv[-3:] == ["remote", "get-url", "origin"]:
    print("git@github.com:knowyetai/new-api.git")
else:
    os.execv(os.environ["REAL_GIT"], [os.environ["REAL_GIT"], *sys.argv[1:]])
""")
        self.write_tool("docker", """#!/usr/bin/env python3
import json, os, sys
a = sys.argv[1:]
with open(os.environ["DEPLOY_TEST_EVENTS"], "a") as f:
    f.write(json.dumps({"args": a, "cwd": os.getcwd()}) + "\\n")
if a[0] == "compose":
    if "--help" in a:
        print("--wait-timeout")
    elif "ps" in a and "-q" in a:
        print(a[-1])
    elif "up" in a and os.getenv("FAIL_SWITCH"):
        sys.exit(1)
elif a[0] == "inspect":
    fmt = a[a.index("--format") + 1]
    print("healthy")
elif a[0] == "build" and os.getenv("FAIL_BUILD"):
    sys.exit(1)
""")

    def git(self, *args):
        return subprocess.check_output([REAL_GIT, *args], text=True, stderr=subprocess.PIPE)

    def write_tool(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)

    def run_deploy(self, revision=None, **env):
        script = "deploy.sh"
        result = subprocess.run(
            ["bash", str(self.repo / "deploy/production" / script),
             revision or self.revision[:12], "test-host", str(self.deploy)],
            env=dict(self.env, **env), capture_output=True, text=True,
        )
        self.output = result.stdout + result.stderr
        return result.returncode

    def commands(self):
        return [json.loads(line)["args"] for line in self.events.read_text().splitlines()]

    def test_requested_commit(self):
        (self.repo / "release.txt").write_text("uncommitted local change\n")
        self.assertEqual(self.run_deploy(), 0, self.output)
        self.assertEqual(self.git("-C", str(self.source), "rev-parse", "HEAD").strip(), self.revision)
        self.assertEqual((self.source / "release.txt").read_text(), "requested version\n")
        self.assertEqual((self.deploy / ".deployment-successful-commit").read_text().strip(), self.revision)
        build = next(c for c in self.commands() if c[0] == "build")
        self.assertEqual(build[-1], str(self.source))
        self.assertIn(str(self.source / "deploy/production/Dockerfile.cached"), build)
        self.assertFalse((self.source / "SOURCE_COMMIT").exists())

    def test_remote_dirty_checkout_is_preserved(self):
        (self.source / "local-work.txt").write_text("keep me")
        self.assertNotEqual(self.run_deploy(), 0)
        self.assertEqual((self.source / "local-work.txt").read_text(), "keep me")
        self.assertFalse(any(c[0] == "build" for c in self.commands()))
        self.assertEqual((self.deploy / ".env").read_text(), "original\n")

    def test_ignored_file_is_not_built(self):
        (self.source / "ignored-local").write_text("keep me")
        self.assertNotEqual(self.run_deploy(), 0)
        self.assertFalse(any(c[0] == "build" for c in self.commands()))

    def test_unpublished_commit_does_not_switch(self):
        empty = self.root / "empty.git"
        self.git("init", "--bare", "-q", str(empty))
        self.git("-C", str(self.source), "remote", "set-url", "origin", str(empty))
        self.assertNotEqual(self.run_deploy(), 0)
        self.assertFalse(any(c[0] == "build" for c in self.commands()))
        self.assertEqual((self.deploy / ".env").read_text(), "original\n")

    def test_build_failure_keeps_running_configuration(self):
        self.assertNotEqual(self.run_deploy(FAIL_BUILD="1"), 0)
        self.assertEqual((self.deploy / ".env").read_text(), "original\n")
        self.assertFalse((self.deploy / ".deployment-switching").exists())

    def test_failed_deployment_can_be_replaced_with_previous_hash(self):
        self.assertEqual(self.run_deploy(revision=self.baseline), 0, self.output)
        self.assertNotEqual(self.run_deploy(FAIL_SWITCH="1"), 0)
        self.assertEqual((self.deploy / ".deployment-successful-commit").read_text().strip(), self.baseline)
        self.assertIn(self.baseline, self.output)
        self.assertEqual(self.run_deploy(revision=self.baseline), 0, self.output)
        self.assertEqual(self.git("-C", str(self.source), "rev-parse", "HEAD").strip(), self.baseline)
        self.assertIn("NEW_API_IMAGE=knowyet-new-api:" + self.baseline[:12], (self.deploy / ".env").read_text())

    def test_branch_name_is_rejected_before_ssh(self):
        self.assertNotEqual(self.run_deploy(revision="main"), 0)
        self.assertFalse(self.events.exists())


if __name__ == "__main__":
    unittest.main()
