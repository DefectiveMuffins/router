import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
ANALYZER = ROOT / ".agents/skills/pr-merge-ready/scripts/pr-fix-plan.sh"
FAKE_GH = r'''#!/usr/bin/env python3
import json
import os
import sys

args = sys.argv[1:]


def emit(payload):
    print(json.dumps(payload))


def argument_value(prefix):
    return next((arg[len(prefix):] for arg in args if arg.startswith(prefix)), "")


def comment(index):
    return {
        "body": f"reply-{index:03d}",
        "url": f"https://example.invalid/review/{index}",
        "author": {"login": "fixture-reviewer"},
    }


if args[:2] == ["pr", "diff"]:
    sys.stdout.write(
        "diff --git a/src/fixture.go b/src/fixture.go\n"
        "index 0000000..1111111 100644\n"
        "--- a/src/fixture.go\n"
        "+++ b/src/fixture.go\n"
        "@@ -0,0 +1 @@\n"
        "+package fixture\n"
    )
elif args[:2] == ["pr", "checks"]:
    emit([])
elif args[:2] == ["api", "graphql"]:
    count = int(os.environ.get("THREAD_COMMENT_COUNT", "101"))
    query = argument_value("query=")
    if "$threadId" in query:
        start = int(argument_value("after=cursor-") or "0")
        end = min(start + 100, count)
        emit({
            "data": {
                "node": {
                    "comments": {
                        "nodes": [comment(index) for index in range(start, end)],
                        "pageInfo": {
                            "hasNextPage": end < count,
                            "endCursor": f"cursor-{end}",
                        },
                    }
                }
            }
        })
    else:
        emit({
            "data": {
                "repository": {
                    "pullRequest": {
                        "headRefName": "fixture-branch",
                        "headRefOid": "a" * 40,
                        "reviewThreads": {
                            "nodes": [{
                                "id": "THREAD_FIXTURE",
                                "isResolved": False,
                                "isOutdated": False,
                                "path": "src/fixture.go",
                                "line": 1,
                                "startLine": 1,
                                "comments": {
                                    "nodes": [comment(index) for index in range(min(count, 100))],
                                    "pageInfo": {
                                        "hasNextPage": count > 100,
                                        "endCursor": "cursor-100" if count > 100 else "",
                                    },
                                },
                            }],
                            "pageInfo": {"hasNextPage": False, "endCursor": "thread-cursor"},
                        },
                    }
                }
            }
        })
else:
    route = next((arg for arg in args if arg.startswith("repos/")), "")
    if "/pulls/" in route and "/files?" in route:
        emit([
            {"filename": "src/fixture.go"},
            {"filename": "deleted.txt"},
            {"filename": "assets/fixture.bin"},
        ])
    elif "/reviews?" in route:
        if os.environ.get("FAIL_REVIEWS") == "1":
            print("synthetic review-fetch failure", file=sys.stderr)
            sys.exit(1)
        emit([])
    elif "/issues/" in route and "/comments?" in route:
        if os.environ.get("FAIL_CONVERSATION_COMMENTS") == "1":
            print("synthetic conversation-fetch failure", file=sys.stderr)
            sys.exit(1)
        emit([])
    else:
        print(f"unexpected gh invocation: {args!r}", file=sys.stderr)
        sys.exit(1)
'''


class PRFixPlanTest(unittest.TestCase):
    def run_analyzer(self, **extra_env):
        with tempfile.TemporaryDirectory() as directory:
            bin_dir = Path(directory) / "bin"
            bin_dir.mkdir()
            fake_gh = bin_dir / "gh"
            fake_gh.write_text(FAKE_GH)
            fake_gh.chmod(0o755)
            env = os.environ.copy()
            env["PATH"] = str(bin_dir) + os.pathsep + env["PATH"]
            env["THREAD_COMMENT_COUNT"] = "101"
            env.update(extra_env)
            return subprocess.run(
                [
                    str(ANALYZER),
                    "1",
                    "--owner",
                    "fixture-owner",
                    "--repo",
                    "fixture-repo",
                    "--max-comments",
                    "0",
                    "--json",
                ],
                cwd=ROOT,
                env=env,
                capture_output=True,
                text=True,
                timeout=30,
            )

    def test_plan_includes_all_thread_replies_and_changed_paths(self):
        analyzer_result = self.run_analyzer()
        self.assertEqual(0, analyzer_result.returncode, analyzer_result.stderr)
        plan = json.loads(analyzer_result.stdout)

        self.assertEqual(
            ["assets/fixture.bin", "deleted.txt", "src/fixture.go"],
            plan["changed_files"],
        )
        thread = plan["unresolved_comments"][0]
        self.assertEqual(101, thread["thread_comments"])
        self.assertEqual(101, len(thread["comments"]))
        self.assertEqual("reply-100", thread["comments"][-1]["body"])
        self.assertIn("reply-100", thread["body"])

    def test_feedback_fetch_failures_are_reported_in_successful_plan(self):
        analyzer_result = self.run_analyzer(
            FAIL_REVIEWS="1", FAIL_CONVERSATION_COMMENTS="1"
        )
        self.assertEqual(0, analyzer_result.returncode, analyzer_result.stderr)
        plan = json.loads(analyzer_result.stdout)

        self.assertTrue(plan["reviews_fetch_failed"])
        self.assertTrue(plan["conversation_comments_fetch_failed"])
        self.assertNotIn("top_level_comments", plan)


if __name__ == "__main__":
    unittest.main()
