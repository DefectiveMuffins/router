import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "agent_checks", Path(__file__).with_name("agent_checks.py")
)
CHECKS = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = CHECKS
SPEC.loader.exec_module(CHECKS)


def fixture_git(root: Path, *arguments: str) -> str:
    # PATH may contain a git wrapper that writes notes asynchronously after
    # commits. Keep fixture mutations isolated from those user integrations.
    executable = (
        Path(subprocess.check_output(["git", "--exec-path"], text=True).strip()) / "git"
    )
    return (
        subprocess.check_output(
            [
                str(executable),
                "-c",
                "core.hooksPath=/dev/null",
                "-c",
                "commit.gpgsign=false",
                *arguments,
            ],
            cwd=root,
            stderr=subprocess.DEVNULL,
        )
        .decode()
        .strip()
    )


class AgentChecksTest(unittest.TestCase):
    def test_new_execution_boundaries_require_smoke(self):
        for path in (
            "internal/dispatch/executor.go",
            "internal/sse/frame.go",
            "internal/router/policy/plan.go",
            "internal/gateway/gateway.go",
        ):
            with self.subTest(path=path):
                self.assertIn(
                    CHECKS.SuiteID.SMOKE,
                    {suite.id for suite in CHECKS.select_suites([path])},
                )

    def test_docs_do_not_require_inference_or_docker(self):
        self.assertEqual(
            [CHECKS.SuiteID.DOCS],
            [suite.id for suite in CHECKS.select_suites(["docs/SEMANTICS.md"])],
        )

    def test_unknown_diff_fails_closed(self):
        self.assertEqual(len(CHECKS.SUITES), len(CHECKS.select_suites(None)))
        self.assertEqual([], CHECKS.select_suites([]))

    def test_diff_includes_staged_unstaged_and_untracked_not_ignored(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def git(*args):
                return fixture_git(root, *args)

            git("init")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "user.name", "Fixture")
            (root / ".gitignore").write_text("ignored\n")
            (root / "tracked").write_text("original")
            git("add", ".")
            git("commit", "-m", "fixture")
            base = git("rev-parse", "HEAD")
            (root / "tracked").write_text("modified")
            (root / "staged").write_text("staged")
            git("add", "staged")
            (root / "new").write_text("new")
            (root / "ignored").write_text("private")
            self.assertEqual(
                ["new", "staged", "tracked"], CHECKS.changed_paths(root, base)
            )
            self.assertIsNone(CHECKS.changed_paths(root, "not-a-revision"))
            self.assertIsNone(CHECKS.changed_paths(root, "", base))
            self.assertIsNone(CHECKS.changed_paths(root, base, ""))
            self.assertIsNone(CHECKS.changed_paths(root, "  ", base))

    def test_explicit_head_uses_merge_base_and_excludes_dirty_checkout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def git(*args):
                return fixture_git(root, *args)

            git("init")
            git("config", "user.email", "fixture@example.invalid")
            git("config", "user.name", "Fixture")
            (root / "common").write_text("fixture")
            git("add", ".")
            git("commit", "-m", "common fixture")
            ancestor = git("rev-parse", "HEAD")
            git("checkout", "-b", "incident-fixture")
            (root / "incident.go").write_text("synthetic change")
            git("add", ".")
            git("commit", "-m", "incident fixture")
            head = git("rev-parse", "HEAD")
            git("checkout", "-b", "upstream-fixture", ancestor)
            (root / "upstream.md").write_text("unrelated upstream change")
            git("add", ".")
            git("commit", "-m", "upstream fixture")
            base = git("rev-parse", "HEAD")
            (root / "dirty").write_text("unrelated working tree change")
            (root / "common").write_text("unrelated staged change")
            git("add", "common")
            self.assertEqual(["incident.go"], CHECKS.changed_paths(root, base, head))

    def test_unavailable_git_diff_is_unclassifiable_not_empty(self):
        with patch.object(
            CHECKS.subprocess, "run", side_effect=subprocess.TimeoutExpired("git", 30)
        ):
            self.assertIsNone(CHECKS.changed_paths(Path.cwd(), "origin/main"))

    def test_doctor_never_invokes_commands_or_reads_env_files(self):
        with patch.object(
            CHECKS.subprocess, "run", side_effect=AssertionError("unexpected command")
        ):
            report = CHECKS.doctor(Path(__file__).resolve().parents[1])
        self.assertTrue(report["files"]["go.mod"])
        self.assertIn("cloud authentication", report["not_checked"])

    def test_make_smoke_does_not_load_developer_env(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_bytes(
                (Path(__file__).resolve().parents[1] / "Makefile").read_bytes()
            )
            (root / ".env.local").write_text(
                "$(error developer env must not be evaluated)\n"
            )
            smoke = subprocess.run(
                ["make", "-n", "smoke"], cwd=root, capture_output=True, text=True
            )
            self.assertEqual(0, smoke.returncode, smoke.stderr)
            mixed = subprocess.run(
                ["make", "-n", "smoke", "build"],
                cwd=root,
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(0, mixed.returncode)
            self.assertIn("separately", mixed.stderr)

    def test_missing_prerequisite_is_blocked_not_passed(self):
        suite = next(suite for suite in CHECKS.SUITES if suite.id == CHECKS.SuiteID.GO)
        with patch.object(CHECKS.shutil, "which", return_value=None):
            receipt = CHECKS.run_suite(Path.cwd(), suite, False)
        self.assertEqual(CHECKS.Outcome.BLOCKED, receipt["outcome"])
        self.assertEqual([], receipt["commands"])

    def test_integration_requires_explicit_opt_in(self):
        suite = next(
            suite for suite in CHECKS.SUITES if suite.id == CHECKS.SuiteID.SMOKE
        )
        with patch.object(
            CHECKS.shutil, "which", return_value="available"
        ), patch.object(CHECKS.subprocess, "run") as run:
            receipt = CHECKS.run_suite(Path.cwd(), suite, False)
        run.assert_not_called()
        self.assertEqual(CHECKS.Outcome.BLOCKED, receipt["outcome"])

    def test_failed_check_is_not_hidden_by_later_checks(self):
        suite = next(
            suite for suite in CHECKS.SUITES if suite.id == CHECKS.SuiteID.DOCS
        )
        with patch.object(
            CHECKS.shutil, "which", return_value="available"
        ), patch.object(
            CHECKS.subprocess, "run", return_value=subprocess.CompletedProcess([], 1)
        ):
            receipt = CHECKS.run_suite(Path.cwd(), suite, False)
        self.assertEqual(CHECKS.Outcome.FAILED, receipt["outcome"])
        self.assertEqual(1, len(receipt["commands"]))

    def test_real_children_cannot_adopt_ambient_credentials_or_live_test_flags(self):
        poisoned = {
            "ANTHROPIC_API_KEY": "synthetic-provider-secret",
            "GOOGLE_API_KEY": "synthetic-provider-secret",
            "OPENAI_API_KEY": "synthetic-provider-secret",
            "GH_TOKEN": "synthetic-source-control-secret",
            "AWS_SECRET_ACCESS_KEY": "synthetic-cloud-secret",
            "GOOGLE_APPLICATION_CREDENTIALS": "/synthetic/private/cloud.json",
            "DATABASE_URL": "postgres://synthetic.invalid/shared",
            "ROUTER_TEST_DATABASE_URL": "postgres://synthetic.invalid/shared",
            "PGPASSWORD": "synthetic-db-secret",
            "ROUTER_POLICY_LIVE_TEST_URL": "https://synthetic.invalid/live",
            "ESCALATION_TEST_URL": "https://synthetic.invalid/live",
            "ESCALATION_TEST_FIXTURE": "/synthetic/private/capture.json",
            "HMM_CONTRACT_PYTHON": "/synthetic/private/python",
            "HMM_CONTRACT_EXPORT": "/synthetic/private/export.json",
            "HMM_TEST_PACKAGE": "/synthetic/private/artifact.json",
            "GOFLAGS": "-tags=google_integration -run=^$ -exec=/synthetic/private/tool",
            "UV_ENV_FILE": "/synthetic/private/.env.local",
            "INSTALLER": "/synthetic/private/install.sh",
            "UNINSTALLER": "/synthetic/private/uninstall.sh",
            "BASH_ENV": "/synthetic/private/startup.sh",
            "PYTEST_ADDOPTS": "--synthetic-skip-checks",
            "NODE_OPTIONS": "--require=/synthetic/private/hook.js",
            "SMOKE_PROXY_MODE": CHECKS.ProxyMode.RECORD.value,
            "SMOKE_BASE_URL": "https://synthetic.invalid/production",
            "SMOKE_KEEP_STACK": "1",
            "SMOKE_RECORD_ANTHROPIC_KEY": "synthetic-recording-secret",
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable_dir = root / "bin"
            executable_dir.mkdir()
            probe_output = root / "probe.jsonl"
            fields = list(poisoned) + ["GOCACHE", "ROUTER_ONNX_ASSETS_DIR"]
            probe = (
                f"#!{sys.executable}\nimport json, os, sys\nfrom pathlib import Path\n"
                f"observed = {{key: os.environ[key] for key in {fields!r} if key in os.environ}}\n"
                "with Path(os.environ['VALIDATION_PROBE_OUTPUT']).open('a') as output:\n"
                "    output.write(json.dumps({'argv': sys.argv, 'environment': observed}) + '\\n')\n"
            )
            for executable in (
                "go",
                "bash",
                "python3",
                "node",
                "jq",
                "curl",
                "uv",
                "docker",
            ):
                path = executable_dir / executable
                path.write_text(probe)
                path.chmod(0o755)
            installer_tests = root / "install/tests"
            installer_tests.mkdir(parents=True)
            (installer_tests / "offline_test.sh").write_text(
                "# synthetic command destination\n"
            )
            inherited_cache = os.environ.get("GOCACHE")
            fixture_environment = poisoned | {
                "PATH": f"{executable_dir}:{os.environ['PATH']}",
                "VALIDATION_PROBE_OUTPUT": str(probe_output),
                "ROUTER_ONNX_ASSETS_DIR": "/synthetic/native/assets",
            }
            with patch.dict(os.environ, fixture_environment):
                for suite_id in (
                    CHECKS.SuiteID.GO,
                    CHECKS.SuiteID.HMM,
                    CHECKS.SuiteID.INSTALL,
                    CHECKS.SuiteID.SMOKE,
                ):
                    suite = next(
                        suite for suite in CHECKS.SUITES if suite.id == suite_id
                    )
                    receipt = CHECKS.run_suite(root, suite, True)
                    self.assertTrue(receipt["commands"], suite_id)
                    self.assertTrue(
                        all(
                            command["exit_code"] == 0 for command in receipt["commands"]
                        ),
                        suite_id,
                    )
            probes = [
                json.loads(line) for line in probe_output.read_text().splitlines()
            ]
            self.assertEqual(7, len(probes))  # Go x3, HMM x1, installer x2, smoke x1.
            for observed in probes:
                environment = observed["environment"]
                self.assertEqual(inherited_cache, environment.get("GOCACHE"))
                self.assertEqual(
                    "/synthetic/native/assets", environment["ROUTER_ONNX_ASSETS_DIR"]
                )
                self.assertEqual("-mod=readonly", environment["GOFLAGS"])
                for key in poisoned.keys() - {"GOFLAGS", "SMOKE_PROXY_MODE"}:
                    self.assertNotIn(key, environment, observed["argv"])
                if observed["argv"][-1] == "scripts/smoke/run.sh":
                    self.assertEqual(
                        CHECKS.ProxyMode.REPLAY.value, environment["SMOKE_PROXY_MODE"]
                    )
                else:
                    self.assertNotIn("SMOKE_PROXY_MODE", environment)
            hmm_command = next(
                observed["argv"]
                for observed in probes
                if Path(observed["argv"][0]).name == "uv"
            )
            self.assertIn("--no-env-file", hmm_command)


if __name__ == "__main__":
    unittest.main()
