"""Tests for clone_layout — per-clone directories under the git dir (ADR-024 § 7)."""

import os
import subprocess
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from clone_layout import NotAGitRepositoryError, clone_class_dir  # noqa: E402


def _git(*args, cwd):
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True)


@pytest.fixture
def repo(tmp_path):
    root = tmp_path / "repo"
    root.mkdir()
    _git("init", "-q", cwd=root)
    return root


def test_resolves_under_the_git_directory(repo):
    want = os.path.join(os.path.realpath(repo), ".git", "nightgauge", "pipeline")
    assert clone_class_dir(str(repo), "pipeline") == want


def test_linked_worktree_resolves_to_the_main_clone(repo, tmp_path):
    env = {
        **os.environ,
        "GIT_AUTHOR_NAME": "t",
        "GIT_AUTHOR_EMAIL": "t@example.com",
        "GIT_COMMITTER_NAME": "t",
        "GIT_COMMITTER_EMAIL": "t@example.com",
    }
    subprocess.run(
        ["git", "commit", "-q", "--allow-empty", "-m", "init"],
        cwd=repo, check=True, capture_output=True, env=env,
    )
    wt = tmp_path / "wt"
    _git("worktree", "add", "-q", str(wt), cwd=repo)
    assert clone_class_dir(str(wt), "logs") == clone_class_dir(str(repo), "logs")


def test_ignores_an_inherited_git_dir(repo, tmp_path, monkeypatch):
    other = tmp_path / "other"
    other.mkdir()
    _git("init", "-q", cwd=other)
    monkeypatch.setenv("GIT_DIR", str(other / ".git"))
    want = os.path.join(os.path.realpath(repo), ".git", "nightgauge", "retros")
    assert clone_class_dir(str(repo), "retros") == want


def test_outside_a_repository_fails(tmp_path, monkeypatch):
    monkeypatch.setenv("GIT_CEILING_DIRECTORIES", str(tmp_path))
    plain = tmp_path / "plain"
    plain.mkdir()
    with pytest.raises(NotAGitRepositoryError, match="not a git repository"):
        clone_class_dir(str(plain), "pipeline")


def test_unknown_class_is_rejected(repo):
    with pytest.raises(ValueError):
        clone_class_dir(str(repo), "knowledge")
