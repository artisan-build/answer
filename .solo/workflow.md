# Workflow — answer

Unofficial public Laravel Cloud deployment wrapper for Apache Answer. This repository packages the
official Apache Answer release with the fixed launcher behavior Scalpels can realize. It is not an
Apache Software Foundation project and is not endorsed by the ASF.

## Phase and mode

- phase: pre-launch
- default mode: A-autonomous
- merge policy: merge on green CI after the coordinator gate, independent quality review, and acceptance judge
- merge method: `gh pr merge --squash --delete-branch`

## Hard gate

- local: `bash -n build.sh`; `test -z "$(gofmt -l .)"`; `go vet ./...`; `staticcheck ./...`; `go test ./...`
- CI: `.github/workflows/ci.yml`, required check `Hard gate`
- production-platform smoke: CI builds the pinned Answer release, boots the wrapper against MySQL 8.4, and asserts HTTP 200

Implementers run focused tests plus lint/static analysis once. The coordinator runs the complete local gate once on the committed candidate. The Linux Answer build/smoke remains CI authority when the local architecture cannot execute it.

## Dependency install

- `go mod download`
- install Staticcheck with `go install honnef.co/go/tools/cmd/staticcheck@latest` when it is not already available

## Ship details

- branch naming: `feat/<slug>`, `fix/<slug>`, `chore/<slug>`
- PR target repo: `artisan-build/answer`
- prose: run `python3 ~/Herd/brain/skills/human-writing/scripts/check-tells.py <file>` before and after README or other reader-facing prose changes, and report both counts
- never commit `.cloud/config.json`, Cloud resource IDs, credentials, deployment-specific domains, or source-site configuration
- commit footer: `Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>`

## Coordination

- Solo project: `answer` (127)
- worktrees: `~/Library/Caches/solo-worktrees/answer-<branch-slug>/`
- the wrapper is seeded from a private production wrapper as source material only; do not modify that repository or its deployment

## Security

- Never write live credentials or raw Cloud responses to disk, logs, test fixtures, or reports.
- Owner passwords are generated inside the target at reveal time, never accepted through argv or Cloud environment variables, and printed only by the bounded rotation command for the one response that requested it.
- Cloud-injected MySQL, Redis and S3 values may be read to configure Answer but must never be logged.
- Do not run mutation testing in this build.
