# Issue tracker: Local Markdown

Issues and PRDs for this repo live as markdown files in `.scratch/`.
（远端是 GitHub `xltxb/AegisDB`（`origin`，推 `main`）。功能 PRD 与实现工单以本地 markdown 形式管理；代码审查发现的缺陷与规范问题记在 GitHub Issues，用 `gh issue` 读写。）

## Conventions

- One feature per directory: `.scratch/<feature-slug>/`
- The PRD is `.scratch/<feature-slug>/PRD.md`
- Implementation issues are `.scratch/<feature-slug>/issues/<NN>-<slug>.md`, numbered from `01`
- Triage state is recorded as a `Status:` line near the top of each issue file (see `triage-labels.md` for the role strings)
- Comments and conversation history append to the bottom of the file under a `## Comments` heading

## When a skill says "publish to the issue tracker"

Create a new file under `.scratch/<feature-slug>/` (creating the directory if needed).

## When a skill says "fetch the relevant ticket"

Read the file at the referenced path. The user will normally pass the path or the issue number directly.
