# Issue tracker: GitHub Issues + local Markdown

| What | Where |
| ---- | ----- |
| Problems to fix (bugs, security, stale docs, code smells) | GitHub Issues on `xltxb/AegisDB` |
| Feature PRDs and their implementation issues | Local markdown in `.scratch/` (gitignored, never committed) |

## Fixing a problem: issue → branch → PR → delete branch

1. **Open an issue** with `gh issue create`: symptom, reproduction steps, impact. Search first (`gh issue list --state all --search "..."`) so as not to file a duplicate.
2. **Branch** from the latest `main`: `fix/<slug>` (or `docs/<slug>`, `chore/<slug>`). Never commit to `main` directly.
3. **Open a PR** against `main` with `gh pr create`. Put `Closes #<N>` in the body, and say which tests ran and what was not verified.
4. **Merge through the PR** with a merge commit, once the relevant tests pass. Don't merge locally and push `main`.
5. **Delete the branch**, local and remote, right after the merge (`gh pr merge <N> --merge --delete-branch`). No need to ask; check that the PR shows as merged and that `main` contains the commits.

The repo is **public**. For an exploitable vulnerability, ask the user before putting any detail in an issue, branch name, commit or PR.

Labels on GitHub: type (`bug`, `security`, `documentation`, ...), area (`backend`, `frontend`, `migration`) and the triage roles in `triage-labels.md`. Check `gh label list` first: not every triage role exists there as a label.

## Feature PRDs and implementation issues

- One feature per directory: `.scratch/<feature-slug>/`
- The PRD is `.scratch/<feature-slug>/PRD.md`
- Implementation issues are `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01`
- Triage state is recorded as a `Status:` line near the top of each issue file (see `triage-labels.md` for the role strings)
- Comments and conversation history append to the bottom of the file under a `## Comments` heading

## When a skill says "publish to the issue tracker"

A problem to fix: create a GitHub issue. A feature PRD or its breakdown: create a new file under `.scratch/<feature-slug>/` (creating the directory if needed).

## When a skill says "fetch the relevant ticket"

`#<N>` or a GitHub URL: `gh issue view <N> --comments`. A path: read the file. The user will normally pass one of these.
