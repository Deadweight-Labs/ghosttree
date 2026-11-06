#!/usr/bin/env python3
"""Validiert die verdeckten NurLearn-Checks (Ausgangsstand / Referenz / naiv)."""
import os
import shutil
import subprocess
import sys
from pathlib import Path

os.environ["BENCH_PROJECT"] = "nurlearn"
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "harness"))
import bench  # noqa: E402

ROOT = Path("/tmp/bench-validate-nl")
ENV = dict(os.environ, GOTOOLCHAIN="local", TMPDIR="/tmp", GIT_CONFIG_NOSYSTEM="1",
           GOMODCACHE=str(bench.SHARED / "gomod"), GOCACHE=str(bench.SHARED / "gocache"))

KP = '''package check

import (
	"context"
	"strings"

	"github.com/NurRobin/NurLearn/internal/lab"
	"github.com/NurRobin/NurLearn/internal/task"
)

func init() { Register("kernel_param", kernelParamChecker{}) }

type kernelParamChecker struct{}

func (kernelParamChecker) Run(ctx context.Context, e Execer, l lab.Lab, spec task.CheckSpec) Result {
	key := StringParam(spec, "key", "")
	want := StringParam(spec, "value", "")
	res, err := e.Exec(ctx, l, PREFIX"sysctl", "-n", key}, Timeout: defaultTimeout})
	if err != nil || res.ExitCode != 0 {
		return Unknown(spec.ID, key+" = "+want, "nicht lesbar")
	}
	got := strings.TrimSpace(res.Stdout)
	if got == want {
		return Pass(spec.ID, key+" = "+want, got)
	}
	return Fail(spec.ID, key+" = "+want, got)
}
'''

UG = '''package check

import (
	"context"
	"strings"

	"github.com/NurRobin/NurLearn/internal/lab"
	"github.com/NurRobin/NurLearn/internal/task"
)

func init() { Register("user_in_group", userInGroupChecker{}) }

type userInGroupChecker struct{}

func (userInGroupChecker) Run(ctx context.Context, e Execer, l lab.Lab, spec task.CheckSpec) Result {
	user := StringParam(spec, "user", "")
	group := StringParam(spec, "group", "")
	res, err := e.Exec(ctx, l, PREFIX"id", "-nG", user}, Timeout: defaultTimeout})
	if err != nil || res.ExitCode != 0 {
		return Unknown(spec.ID, user+" in "+group, "Benutzer unbekannt")
	}
	for _, g := range strings.Fields(res.Stdout) {
		if g == group {
			return Pass(spec.ID, user+" in "+group, res.Stdout)
		}
	}
	return Fail(spec.ID, user+" in "+group, res.Stdout)
}
'''


def fresh(name):
    d = ROOT / name
    if d.exists():
        shutil.rmtree(d)
    shutil.copytree(bench.P["template"], d)
    for c in (["git", "init", "-q"], ["git", "add", "-A"], ["git", "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-qm", "init"]):
        subprocess.run(c, cwd=d, check=True, env=ENV)
    return d, subprocess.run(["git", "rev-parse", "HEAD"], cwd=d, capture_output=True, text=True).stdout.strip()


def apply(d, task, ok):
    prefix = 'lab.Command{Args: []string{"env", "LC_ALL=C", ' if ok else "lab.Command{Args: []string{"
    src, name, typ = (KP, "kernel_param.go", "kernel\\_param") if task == "NF1" else (UG, "user_in_group.go", "user\\_in\\_group")
    (d / "internal/check" / name).write_text(src.replace("PREFIX", prefix))
    if ok:
        doc = d / "docs/PACK-FORMAT.md"
        doc.write_text(doc.read_text() + f"\n\n### {typ}\n\nNeu.\n")


def main():
    allok = True
    for task in ("NF1", "NF2"):
        for variant in ("baseline", "reference", "naive"):
            d, sha = fresh(f"{task}-{variant}")
            if variant != "baseline":
                apply(d, task, variant == "reference")
            g = bench.grade_task(task, d, sha, ENV)
            exp = {"baseline": (False, False), "reference": (True, True), "naive": (True, False)}[variant]
            good = (g["func"], g["know"]) == exp
            allok &= good
            print(task, variant, g["func"], g["know"], "OK" if good else "UNEXPECTED", g["checks"])
            if not good:
                print(g.get("hidden_output_tail", "")[-1500:], g.get("repo_tests_tail", "")[-800:])
    print("ALL OK" if allok else "FAILURES")


if __name__ == "__main__":
    main()
