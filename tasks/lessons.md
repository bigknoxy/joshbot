# Lessons Learned

## 2026-05-17: CI failed on gofmt formatting after v1.19.0 release

**Failure mode**: Pushed commit `1822402` (feat: intelligent memory system and skill self-creation) to main. CI run #108 failed on the "Check formatting" step. Release workflow (v1.19.0 tag) also failed on the test job.

**Detection signal**: CI `gofmt -l .` returned `internal/learning/learning.go` which had vertical-alignment padding in a struct literal that `gofmt` rejects.

**Root cause**: The code-simplifier subagent restructured `internal/learning/learning.go` and introduced non-standard padding on a struct literal field:
```go
Model:       c.provider.Config().Model,  // BAD: extra spaces
Model: c.provider.Config().Model,        // CORRECT: gofmt standard
```

**Contributing factors**:
1. No `gofmt` check in the local verification step (only `go build` + `go test -race`)
2. The release tag was pushed *before* CI on the main push completed — tag pushed at commit `1822402`, CI failed ~30s later
3. The release workflow (`.github/workflows/release.yml`) runs tests independently from CI but uses the same commit, so it also fails

**Prevention rule**: Run `gofmt -d .` (check only, exit non-zero if diffs exist) as part of pre-commit or pre-push verification. Add to AGENTS.md checklist.

**Timeline**:
- 19:26: Pushed commit to main + v1.19.0 tag
- 19:27: CI run #108 failed on "Check formatting"
- 19:29: Release run #5 failed on "Test" job (depended on same commit)
- 19:31: Root cause identified (gofmt in learning.go)
- 19:32: Fixed with `gofmt -w internal/learning/learning.go`, pushed fix commit

## Commit workflow expectations
- CI must pass for ALL commits on main before cutting a release tag
- Release tags must only be pushed AFTER CI on the corresponding main commit is green
- Never push a tag and main commit simultaneously — wait for CI confirmation first
