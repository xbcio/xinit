# xinit

A command-line tool that generates new projects from a "template project + declarative manifest" and **incrementally syncs** template changes into derived projects as the template evolves.

Syncing is a three-way comparison (template baseline `base` / new template version `theirs` / your disk `ours`): template changes are merged in, your local changes are kept as-is; when both sides changed the same spot it **stops and reports a conflict** — you resolve it by hand, then declare convergence with one command. It never silently overwrites your files.

Typical setting: a team/internal template repository (service skeleton, CI config, deployment manifests) with many derived projects. Change the template once, and every derived project picks the update up locally with `xinit update`.

- A derived project **need not be a git repository** (having git is nicer: an extra layer of version control on top of backups).
- No need to touch the tracked `.gitignore`: `init` / `update` idempotently add `.xinit/` to `.git/info/exclude`.
- Dependency: **git ≥ 2.38** (hard dependency, checked at startup — exit if unmet; `changelog` excepted); hooks additionally need a POSIX `sh` on PATH (Git for Windows ships one).

## Table of contents

1. [Introduction](#1-introduction)
2. [Building and installing](#2-building-and-installing)
3. [Getting started](#3-getting-started)
4. [Command surface](#4-command-surface)
5. [Template manifest `xinit.yml` reference](#5-template-manifest-xinityml-reference)
6. [Conflicts and convergence](#6-conflicts-and-convergence)
7. [The `.xinit/` directory](#7-the-xinit-directory)
8. [Exit codes](#8-exit-codes)
9. [Backflow](#9-backflow)
10. [Hooks and trust](#10-hooks-and-trust)
11. [Known limitations](#11-known-limitations)
12. [Development](#12-development)

## 1. Introduction

Templates come in two styles, which can be mixed:

- **A live, runnable project**: files contain no placeholders; the manifest's `substitute` rules rewrite "identity literals" (the template's own brand name, module path, …).
- **A skeleton project**: files use `{{ .var }}` and are rendered via Go templates, opted in per path (the manifest's `render` section).

Once `xinit` has brought your project **under management** (the `.xinit/` state directory), every managed path records "the template content at the last sync". From then on:

```
change the template → tag it → in the derived project run xinit diff (preview) → xinit update (apply)
```

`update` reports an action per path (write / delete / keep local version with a warning / conflict) plus a summary; the template version of conflicting paths is written to `.xinit/conflicts/`.

## 2. Building and installing

Requires **Go 1.25 or newer** (per the `go` declaration in `go.mod`).

**Runtime dependencies**: `git ≥ 2.38` (hard check at startup — exit if unmet; `changelog`, which only reads embedded text, is excepted) and a POSIX `sh` (for hooks; on Windows, Git for Windows' bundled sh). See [Known limitations](#11-known-limitations).

```bash
# build from source into the current directory
go build -o xinit .

# install into $GOBIN (or $GOPATH/bin)
go install .

# or via the Makefile
make build      # → bin/xinit
make install    # → go install .
```

See [Development](#12-development) for all `make` targets.

## 3. Getting started

Every command below comes from a real session (macOS + git 2.50.1) and can be followed along directly. xinit's messages are currently emitted in Chinese; the output blocks below show them rendered in English for readability.

### 3.1 Prepare a template repository

A template repository is an ordinary git repository with an `xinit.yml` at its root. Minimal working manifest (`xkitora` is the template's own identity literal, and will be substituted away):

```yaml
manifest_version: 1

vars:
  - name: app_slug
    default: "demo-service"
    pattern: '^[a-z][a-z0-9-]*$'

substitute:
  - from: "xkitora"
    to: "{{ .app_slug }}"
    paths: ["**/*"]

render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
```

Plus a `go.mod`, a `main.go` (containing the literal `xkitora`) and a `README.md.tmpl` (containing `{{ .app_slug }}`):

```
# go.mod
module example.com/xkitora

go 1.25
```

```
# main.go
// Package main is the entry point of the xkitora service.
package main

import "fmt"

func main() {
	fmt.Println("xkitora service starting")
}
```

```
# README.md.tmpl
# {{ .app_slug }}

Generated from the minimal template.
```

Commit and **tag**:

```bash
git init -q -b main
git add -A
git commit -m "feat: minimal template v1.0.0"
git tag v1.0.0        # always tag; why is explained in 3.3
```

### 3.2 Create a derived project

**It is recommended to make the target directory a git repository first** (then `.xinit/` automatically goes into `.git/info/exclude` and won't be swept into version control by `git add -A`):

**Note that this is still only a recommendation**: a derived project **need not be a git repository** (`init` / `update` do not require one); but **backflow (`backflow`) is opt-in and only available on git-derived projects** — it needs git to enumerate commits and read their content (without touching the working tree), so this one command is unavailable for non-git derivations. To use backflow, `git init` first; details in [Backflow](#9-backflow).

```bash
mkdir order-api && cd order-api
git init -q -b main

xinit init --template $WORK/minimal-template --config $WORK/order-api.vars.yml .
```

`order-api.vars.yml` is the variables file (flat key-value YAML; only the variables you want to override need to be listed):

```yaml
app_slug: order-api
```

Output and result (this session **did** create a git repository):

```
Generated 3 files into /path/to/order-api
```

```
$ cat go.mod
module example.com/order-api

go 1.25

$ cat README.md
# order-api

Generated from the minimal template.

$ tail -2 .git/info/exclude
# *~
.xinit/

$ git status --porcelain --ignored
?? README.md
?? go.mod
?? main.go
!! .xinit/
```

**Note**: `init` accepts `--revision <tag>` to pin a version directly (**recommended**: when you pin a `vX.Y.Z`-form tag — a prerelease suffix is fine too, provided the template also has at least one stable `vX.Y.Z` tag, since default version selection only picks among stable tags — the state records that tag and `update`'s default mode works right away, with no explicit alignment from 3.3). Without it, `init` takes the template repository's **default-branch HEAD** and records it as `"HEAD"`; only then does the first `update` from 3.3 pin the project to a tag. Passing a branch name, commit sha or non-`vX.Y.Z` tag (e.g. `1.0.0`, `release-1`) via `--revision` follows the same rule as omitting the flag (what's recorded is not semver, so the first `update` still needs an explicit `--revision`). A dir source ignores it.

### 3.3 The daily loop (change template → `diff` → `update`)

After `init`, first do one **explicit version alignment** (the closing step for a `"HEAD"` record; skip it if `init` already carried `--revision <vX.Y.Z-form tag>`). The output below comes from a different session that **did not** create a git repository, hence the extra `current directory is not a git repository` warning — create the repository as recommended in 3.2 and it won't appear:

```bash
cd order-api
xinit update --revision v1.0.0
```

```
xinit: warning: current directory is not a git repository: recovery relies solely on .xinit/backup/
xinit update: /path/to/minimal-template
Version: HEAD → v1.0.0
Summary: writes 0, deletes 0, chmod 0, conflicts 0
```

This round needs no template change (all `Noop`); its purpose is to flip the recorded version from `"HEAD"` to `v1.0.0`. **From then on the default mode (no `--revision`) works.** (If `init` carried `--revision <vX.Y.Z-form tag>`, you start in that state and can skip this step.)

> If you did create a git repository as in 3.2 but **have not committed** the files `init` generated, this round adds one more warning:
> `the working tree has 3 uncommitted changes; update rewrites the working tree in place — commit first (or pass --force to skip the check)`
> — the 3 are the three new untracked files (`.xinit/` is in `.git/info/exclude` and not counted). Run `git add -A && git commit` first and the warning is gone.

Then it's the regular loop. Change one thing on the template side and tag it:

```bash
cd $WORK/minimal-template
printf '\n// version: 1.1.0\n' >> main.go
git add -A && git commit -m "feat: print version comment (v1.1.0)" && git tag v1.1.0
```

On the derived side, preview first, then apply (a local change was made to `go.mod` at the same time — a different file from the template's change):

```bash
cd $WORK/order-api
printf '\n// local: build tag\n' >> go.mod

xinit diff          # preview only: exit code 5 = there are actions, 0 = all Noop
```

```
xinit diff: /path/to/minimal-template
Version: v1.0.0 → v1.1.0
  write                main.go
Summary: writes 1, deletes 0, chmod 0, conflicts 0
(diff is a read-only preview: the above is a prediction — no files written, no state changed)
```

```bash
xinit update
```

```
xinit: warning: current directory is not a git repository: recovery relies solely on .xinit/backup/
xinit update: /path/to/minimal-template
Version: v1.0.0 → v1.1.0
  write                main.go
Summary: writes 1, deletes 0, chmod 0, conflicts 0
Backup: .xinit/backup/20261010-094906
```

The template's change landed in `main.go`, and the local change to `go.mod` was kept as-is.

### 3.4 Bring an existing project under management (`adopt`)

If the project already exists but has no `.xinit/` (e.g. it was copied by hand, or the state directory was lost), use `adopt` to rebuild the baseline:

```bash
xinit adopt --template $WORK/minimal-template --config $WORK/order-api.vars.yml
```

```
xinit adopt: /path/to/minimal-template@v1.1.0
Baseline rebuilt: 3 files (.xinit/base/ and .xinit/state.json)
Local changes: 1 file (disk differs from the template render): from update on they are kept as "locally changed" — content edits (disk side is a regular file) go through merge/conflict when the template later touches them too (rows 6/7; row 4 converges silently when the local version already equals the template's new content); for deletions, when the template later touches them too, the deletion is kept with a warning (row 10)
  local-diff           main.go
Warning: if --revision picks the wrong version, the template's changes between
the two versions do not exist on disk and are treated as "the local copy reverted the template" and kept forever — those template updates will never be applied again (§5.3).
```

`--template` and `--config` are required: `vars` is one of the three elements for rebuilding the baseline (template + vars + version) — without it there is no rendered result to compare against.

**`--revision` is not needed** — when omitted, the template's latest semver tag is used (the session above omitted it, got rc=0, and the baseline landed on `v1.1.0`). It is also generally **not recommended**: omitting it gives you "the latest released version", while an explicit pick that turns out wrong means the template's changes between the two versions do not exist on disk and will be treated as "the local copy reverted the template" and kept forever (the consequence the warning above describes). Write it explicitly only when the baseline really must be built on a specific version.

If the template declares hooks, `adopt` neither runs them nor writes a trust record — the next `update` will ask for confirmation as usual.

## 4. Command surface

Apart from `init`, `backflow <commit>`, `restore <path>...` and `changelog [version]`, every command operates on the **current directory** and takes no positional arguments (`restore`'s positional arguments are recorded relative paths inside the project; `changelog`'s positional argument is a version label). `changelog` shows xinit's own changelog and does not depend on the current directory.

> The tables below are a flag quick-reference; **`xinit <command> --help` is authoritative**. For readability the tables omit the internal chapter numbers found in the `--help` text; outputs quoted later in this README do keep such numbers — they are only internal documentation indices and do not affect usage.

### `xinit init [target-dir]` — generate a new project from a template

| Flag | Meaning |
| --- | --- |
| `--template <source>` | Template source (local path or git URL); **required** |
| `--config <file>` | Variables file (flat YAML) |
| `--revision <ref>` | Template version (tag / branch / commit; defaults to the template's default-branch HEAD; ignored by dir sources) |
| `--offline` | No network; read only the local template mirror cache; a missing mirror is an error (no cloning) |
| `--no-hooks` | Skip `post_init` execution (this run's content is still recorded; a later run with hooks still requires confirmation) |
| `--force` | Allow a non-empty target directory (existing same-named files are still not overwritten) |
| `--yes` | Skip hook trust confirmation (same as answering y on an interactive terminal) |
| `--interactive` | Treat as interactive even when stdin is not a terminal: interactive prompts (including trust confirmation) are read line by line from stdin (for scripts and CI) |

The target directory is created if missing; if it exists and is non-empty, `--force` is required; **a directory containing only `.git` counts as empty**. `init` will not re-run on an already-initialized project (use `update` to sync).

**What `--template` points at determines the template-source type** (detected automatically; no extra flag):

- **git source**: a local git repository, or a git URL. It has a version concept (tags / commits) — `update`'s version selection and pinning rely on it.
- **dir source**: a local directory that is **not** a git repository. It has **no version concept** — the state records `kind` as `dir` and `revision` as an empty string, and the version anchor is a **tree hash** of the directory contents (change one file and it is a "new version"). A dir source therefore needs no `--revision` (writing one is meaningless) and no version alignment step from 3.3: in practice, `xinit init` on a dir source followed directly by `xinit update` gives rc=0 with all `Noop`. To track versions cleanly, put the template in a git repository and tag it.

### `xinit update` — sync a derived project to a new template version

| Flag | Meaning |
| --- | --- |
| `--revision <ref>` | Target version (defaults to the template's latest semver tag) |
| `--config <file>` | Variables file (flat YAML; fills in newly added or invalidated vars) |
| `--accept <path>...` | Declare these conflicting paths as manually resolved: record the content of `.xinit/conflicts/<path>` as base |
| `--dry-run` | Print the action list only — nothing written, no state changes |
| `--no-hooks` | Skip `post_update` execution (this run's content is still recorded; a later run with hooks still requires confirmation) |
| `--yes` | Skip hook trust confirmation (same as answering y on an interactive terminal) |
| `--force` | Skip the dirty-working-tree check |
| `--fetch` | Update the template mirror cache over the network |
| `--interactive` | Treat as interactive even when stdin is not a terminal: interactive prompts (including trust confirmation) are read line by line from stdin (for scripts and CI) |

### `xinit diff` — preview what update would do

Runs the exact same code path as `update`, but **writes no files and advances no state**. Exit code: any non-`Noop` action (including predicted conflicts, fold rejections and blockers) → 5; all `Noop` → 0.

| Flag | Meaning |
| --- | --- |
| `--revision <ref>` | Target version (defaults to the template's latest semver tag) |
| `--config <file>` | Variables file (flat YAML) |
| `--fetch` | Update the template mirror cache over the network |

### `xinit status` — read-only report

Four sections: **template side** (currently recorded version, whether an update is available), **local changes** (paths changed relative to base; `generated` paths listed separately), **state health** (prune-list entry count, mixed-EOL file count, engine-version consistency, orphan paths), **missing state** (without `.xinit/`, fail immediately and point to `adopt`).

| Flag | Meaning |
| --- | --- |
| `--fetch` | Update the template mirror cache over the network and check for available updates |

By default it uses **no network** and **writes no files inside the project** (including `.git/info/exclude`; when a template mirror exists, a temporary lock file is created/deleted inside the cached mirror directory). Without `--fetch` the exit code is always 0 (unless an error occurs).

### `xinit template` — read-only view of the template side

Without flags it prints a **template-side summary**: template source, the project's recorded version and commit, and any higher stable semver tags — the same layout as the "template side" section of `status`. To see what upgrading to a new version would bring, add `--changelog`:

```
$ xinit template --changelog
xinit template: /path/to/tpl
Template side:
  Recorded version: v1.0.0 (commit acc24f24f653f937cdc168f43ba8bbc94c7a4c0c)
  Updates available: 2: v1.1.0, v1.2.0 (latest v1.2.0)
Changes (v1.0.0 → v1.2.0):

v1.2.0
  - fix: gamma
  - chore: rc prep

v1.1.0
  - feat: beta
  - feat: alpha
```

| Flag | Meaning |
| --- | --- |
| `--changelog` | Also list the changes between the recorded version and the target version |
| `--revision <ref>` | Endpoint of the changelog range (tag / branch / commit; defaults to the latest stable tag; **must be combined with `--changelog`**) |
| `--fetch` | Refresh an **existing** template mirror cache over the network |

How the changelog is computed:

- The range is "the recorded commit (exclusive) to the target (inclusive)", **sectioned by stable semver tag**, ordered by history with later releases first (not by version number: patch tags on maintenance branches and tags whose version order disagrees with their history position still each land in their place); each section holds that range's commit subjects (no merge commits), newest → oldest. Every commit is listed exactly once, in the earliest section that contains it. Prerelease tags (e.g. `v1.2.0-rc1`) are not listed separately — their commits fold into the next stable section; multiple stable tags on the same commit merge into one section.
- When `--revision` is not a stable tag (branch name, sha, `HEAD`, a prerelease tag like `v1.2.0-rc1`), the final section is named with your original text; it must resolve to a commit (`v1.0.0^{tree}`-style expressions that point at a tree are rejected).
- When the recorded version is not `vX.Y.Z` (e.g. `init` recorded `HEAD` because no version was pinned), the range header shows the 8-character short commit as the starting point.
- When the range has nothing to list you get an explicit explanation rather than empty output: the recorded version is already the latest stable version, is ahead of the latest stable tag, `--revision` points at an older version (a downgrade target), or the two are not on the same line of history.
- When a tag sits on a merge commit and the commits brought in by the merge were all assigned to earlier sections, that section has no subjects and shows a placeholder line "(no non-merge commits)".
- Commit subjects come from the template repository (possibly third-party): control characters (including ESC escape sequences) and Unicode bidi override characters are stripped before output.

**A dir source has no version history**, so `--changelog` fails outright; the summary without `--changelog` works as usual. By default **no network**: only the local mirror cache is read — when the mirror is missing the summary degrades to an explanation and `--changelog` fails (neither clones). **Read-only**: no project lock, no files written inside the project (when a template mirror exists, a temporary lock file is created/deleted inside the cached mirror directory); exit code only 0 / 1 (see §8).

xinit's own changelog is not here — see `xinit changelog` next.

### `xinit changelog [version]` — show xinit's own changelog

Prints the `CHANGELOG.md` embedded in the binary: no argument gives the full text; a version (`1.0.0` or `v1.0.0`; `Unreleased` case-insensitive) gives just that version's section. An unknown version fails and lists the available versions.

No network, no git reads, no dependence on the current directory (works anywhere), and it writes no files; it is the only command that does not need git (the startup check is skipped). Exit codes 0 / 1. A template's own change history lives in `xinit template --changelog`.

### `xinit check` — read-only gate checks

Asserts "`.xinit/` is internally consistent + the git contract holds + the template can keep updating", for consumption by CI and local gates. It runs every check and then summarizes (no stop at the first failure), in four groups: **A state and base tree** (legality of `files` keys, fold collisions, base ↔ recorded-hash correspondence, fragments/non-regular files, binary/mixed_eol/exec bits, template fields), **B leftover conflicts** (`.xinit/conflicts/` non-empty), **C config and git contract** (`prune.txt` globs compile, `.xinit/` not tracked by git, exclude contains `.xinit/`), **D template side** (a higher tag exists in the local mirror, engine-version drift, the recorded commit is fetchable). Each line starts with `FAIL` / `warn` / `skip`, with a summary line at the end.

| Flag | Meaning |
| --- | --- |
| `--fetch` | Refresh the template mirror cache over the network before checking the template side |

**Read-only**: no project lock, no files written inside the project (including `.git/info/exclude` and `conflicts/`), nothing repaired; no mirror cloning, no network by default (when the mirror is missing, the template-side checks degrade to `skip`). Exit code: any `FAIL` → **6**; only `warn` / `skip` → 0 (including "a higher tag exists" under `--fetch` — being behind is not unhealthy; this differs from `status --fetch`'s 5); not an xinit project, or a corrupted `state.json` → 1.

### `xinit adopt` — rebuild the baseline

Records the template's **rendered result** at the given version as base (not the disk contents); all differences between disk and rendered result are treated as local changes.

| Flag | Meaning |
| --- | --- |
| `--template <source>` | Template source (local path or git URL); **required** |
| `--revision <ref>` | Target version (defaults to the template's latest semver tag) |
| `--config <file>` | Variables file (flat YAML); **required** |

### `xinit restore <path>...` — restore recorded paths into the working tree

Restores 1–n recorded paths (keys of `files` in `.xinit/state.json`, i.e. the Dst paths the template has recorded) from `.xinit/base/` into the project's working tree — the command form of `update`'s "recover the version recorded by the template: cp .xinit/base/<path> <path>". `update` is responsible for **advancing to a new version**; `restore` only **brings back the version recorded in base** (neither calls the other). Use it when a file was deleted by mistake, or when you want to roll local changes back to the template version wholesale.

Three-state semantics (every `<path>` must be a recorded path):

- **Target missing** → restore from base (bytes plus exec bit);
- **Target byte-identical to base** → no change (idempotent success, prints "no change: <path>");
- **Target exists with different content** → **refused by default** (exit 1, not a single byte written; the paths are reported with a hint to use `--force`); only with `--force` is it overwritten — resetting the path back to the template's recorded version.

| Flag | Meaning |
| --- | --- |
| `--force` | Overwrite existing files with different content (by default these paths are refused: exit 1, disk untouched) |

**All-or-nothing preflight**: if any path is illegal (absolute path, `..` segment, …), not recorded, missing from base, or refused for overwrite, it exits 1 before writing any bytes. When a file is missing from base, base is **not** rebuilt (that needs the template source and vars): run `xinit update` to rebuild base first, then `restore`.

**Zero network, zero state writes**: no network, no `.xinit/state.json` writes, no `.xinit/base/` writes (only the project lock `.xinit/lock` may appear) — after a restore the path matches base, so the next `update` naturally stops warning about it. Exit codes 0 / 1 (see §8).

**Pruned paths are not recorded**: on the round where `update` takes effect, a pruned path is removed from `.xinit/base/` and from the `state.json` records; `restore` does not apply to it — to bring it back under management see §7.

### `xinit backflow <commit>` — send shared-surface changes back to the template

Translates a commit's changes to the **shared surface** (manifest `upstream.paths` ∪ `--paths`) back into the template's coordinate system and produces patches the template author can `git am` directly; **requires the derived project to be a git repository** (opt-in). The full flow is in [Backflow](#9-backflow).

| Flag | Meaning |
| --- | --- |
| `--paths <glob>` | Shared-surface glob (repeatable; unioned with the manifest's `upstream.paths`) |
| `--map <Dst>=<Src>` | Explicitly give the template-side path (repeatable; the escape hatch for new files with zero/multiple automatic matches, and for paths whose template origin no longer exists) |
| `--out <dir>` | Output directory (default `.xinit/backflow/`; project-relative paths allowed inside the project, absolute paths required outside) |
| `--dry-run` | Run all classification/translation/scanning, but write no output and take no lock |

Exit codes 0 / 1 (see §8); the target commit's **final trailer block** must contain the verbatim line `Template-Upstream: yes`.

> The root command also carries cobra's built-in `completion` and `help` helper commands, unrelated to the business commands above.

### `xinit backup prune --keep <N>` — prune old `.xinit/backup/` copies

Deletes the **older** backup directories in `.xinit/backup/`, keeping only the newest `N` — `update` backs up before every rewrite (see §7), so long-running projects grow `backup/` without bound. Cleanup is therefore an **explicit manual action**: the behavior of `update` / `init` / `adopt` / `restore` / `backflow` is unaffected, and cleanup **never happens automatically**.

- **A "copy" is one backup directory**: only `update`'s backup naming is recognized (`<yyyyMMdd-HHmmss>`; a second one within the same second gets `-2`, `-3`, …, each counting as a copy). The newest `N` are kept in ascending time order and the rest are deleted; other names, plain files and symlinks are never touched (a matching name that is not a directory is also ignored).
- **Deletion happens by default** (deletion is not recoverable — confirm these copies need no rollback before running); if `backup/` does not exist or holds fewer than `N` copies, nothing is deleted (rc=0), and `backup/` is **not created**.

| Flag | Meaning |
| --- | --- |
| `--keep <N>` | Keep the newest N copies (**required**; `N ≥ 1`; `N < 1` fails with exit 1) |
| `--dry-run` | List the copies that would be deleted, without deleting |

**Zero network, zero state writes**: no network, no `.xinit/state.json` writes, no working-tree writes (only the project lock `.xinit/lock` may appear); not an xinit project (no `.xinit/`) fails with exit 1 and will not create `.xinit/` for you. Exit codes 0 / 1 (see §8). To clear all backups, `rm -rf .xinit/backup` by hand (the deletion scope is set explicitly by `--keep`; there is no `--force`).

### Interactive prompts

`init` and `update` prompt you on a **real terminal**; `--interactive` treats stdin as interactive even when it is not a terminal — **all prompts (including trust confirmation) are read line by line from stdin**, for scripts and CI to drive. It only opens the input channel and changes no decision criteria: trust confirmation must still be explicitly answered with `y` / `yes` (or `--yes`); the full hook text is still **printed first**, not skipped, not answered for you; and `--yes` only skips trust confirmation — it does **not** answer vars or conflict prompts for you. The escape hatch in the other direction is unchanged: without `--interactive` and with stdin not a terminal, the trust gate does not prompt — `xinit update < /dev/null` only reports (ending with exit 4 and zero writes when confirmation is needed) and prints no prompt line.

**The prompt order is the answer-stream order**: all prompts in one run share a single stdin, consumed line by line in sequence; scripts construct their input files accordingly.

- `init`: ① one prompt per var (in manifest **declaration order**; skipped when `--config` already provides it) → ② hook trust confirmation.
- `update`: ① one prompt per conflicting path (paths in **byte order**) → ② hook trust confirmation.

**No interactivity, zero prompts**: when stdin is not a terminal and `--interactive` is absent, the tool does not read stdin and prints no interactive text — answers pre-filled into a pipe are ignored (the trust gate also does not ask here and simply treats it as a refusal). To drive prompts in a pipe / CI, add `--interactive`.

**The default answer always equals the status quo** (the tool never does anything irreversible on the default path):

- **Conflicts**: prompts one by one as `<path> (<conflict type>) switch to the template version? [y/N] `. Answering `y` / `yes` = **switch to the template version** — the template version is written to the working tree (your previous content is backed up to `.xinit/backup/` first) and the path converges this round; **pressing Enter alone = keep local**, i.e. the status-quo report and exit 2 from §6. (When entries in `.xinit/conflicts/` are still judged conflicting this round, that round reports and exits early without asking.)
- **vars**: the prompt looks like `<prompt word> [<default>]: ` (without a default: `<prompt word> (required): ` or `<prompt word> (may be left empty): `). Enter alone = take the default; a value that fails `pattern` (empty answers are judged as the default), or an empty answer to a required variable, prints the reason and re-prompts; **end of input (EOF) aborts** — exit 1, no files generated, and the error message names `--config` as the remedy.
- **Trust confirmation**: Enter alone / end of input = refusal (exit 4, zero writes).

**EOF semantics deliberately differ between the two sides**: `init`'s vars collection **aborts** on end of input — silently adopting half an answer set would produce a variable set you never confirmed; `update`'s conflict prompts treat **everything remaining as "keep local"** on end of input (already-answered ones are honored), never losing data.

**Ctrl-C**: while waiting for input, Ctrl-C terminates with the terminal default semantics (exit code 130 from a shell's perspective on unix; decided by the host on Windows), writing no project content — every prompt point lies before the round's first **content** write. Read-only paths never prompt: `--dry-run` never prompts; `diff` does not register `--interactive` (passing it is an unknown flag) and never prompts itself.

## 5. Template manifest `xinit.yml` reference

The manifest sits at the template repository's root. Parsing is strict: **undeclared top-level keys fail immediately**, and every regex (including `pattern` and the predicates) is compiled when the manifest is read — mistakes fail right away instead of at render time.

### `manifest_version` (required)

An integer, currently `1`.

### `vars`

A sequence (not a mapping); per entry:

| Field | Meaning |
| --- | --- |
| `name` | Variable name; must be a valid Go identifier (`[A-Za-z_][A-Za-z0-9_]*`) — it is used as a template field |
| `prompt` | Prompt word for interactive collection: `init` prompts for every variable that declares it and is not provided via `--config` (ignored entirely in non-interactive runs) |
| `default` | Default value; itself rendered as a Go template and may reference already-evaluated variables |
| `pattern` | Go RE2 regex; a user-supplied value must match |
| `required` | When true, the value must not be empty |

- **Variable values are strings only**; `default: false` or `default: 42` in YAML is rejected.
- `default` can use these template functions: `lower`, `upper`, `title`, `replace`, `trim`, `trimSuffix`, `trimPrefix`, `default`.
- **Application order: `--config` values > interactive answers > `default`** (interactive answers can only come from `init`'s [Interactive prompts](#interactive-prompts)); then each `pattern` is checked in turn, and a mismatch fails naming the variable, the actual value and the pattern:

  ```
  xinit: variable "app_slug": value "Order-API" does not match pattern "^[a-z][a-z0-9-]*$"
  ```

### `substitute`

Declarative literal substitution, **evaluated in order** (the two rules below are the ones actually used by this document's demo template):

```yaml
substitute:
  - from: "SvcKit"
    to: "{{ .project_name }}"
    paths: ["**/*.go", "**/*.yaml"]
  - from: "svckit"
    to: "{{ .app_slug }}"
    paths: ["go.mod", "Taskfile.yml", "deploy/**"]
```

| Field | Meaning |
| --- | --- |
| `from` | The literal to replace (**an empty string is rejected by the schema**) |
| `to` | The replacement text, rendered as a Go template |
| `paths` | Only takes effect in template files matching these globs |
| `when_preceded_by` | Predicate: only replace when the text from **the start of this line** up to the match position matches this regex (the classic form `'module[ \t]+'` — changing only the occurrence after `module`) |
| `when_followed_by` | Predicate: only replace when the text from the end of the match to **the end of this line** matches this regex |
| `target_case` | Case transform on the replacement; none by default (`from: "Xkitora"` will not lowercase `Xdesk` into `xdesk`) |

Matching details: the byte before/after `from` must not be `[A-Za-z]` (letter boundaries, so `xkitora` does not match inside `xkitora2`); predicate windows are limited to **the current line** and never span lines. Rules take effect in declaration order; the first matching rule wins at a position.

### `render`

Files matching `paths` are rendered with Go `text/template` (the root data is the variable table, so you write `{{ .app_slug }}`):

```yaml
render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"
```

`strip_suffix` removes that suffix from the target path (`README.md.tmpl` → `README.md`). Paths matching `render` **no longer participate in `substitute`**.

### `generated`

Declares paths "produced by hooks and not mergeable with local changes" (matching the **derived-side target path**):

```yaml
generated:
  paths: ["deploy/bindings.gen.txt"]
```

Semantics: on these paths a template version with **changed content** (where the disk side is a **regular file**) **overwrites** your disk content (your version is kept in `.xinit/overwritten/`; when the local version already equals the template's new content, row 4 converges silently); the files must therefore **be regenerated by hooks after every `init`/`update`** — the two can only be written together, never one side alone. On rounds where hooks do not run (`--no-hooks`, or no `post_update` declared), these paths are skipped entirely (no conflict judgment, disk untouched, local content kept); locally deleted ones follow the deletion branch (row 10).

### `hooks`

```yaml
hooks:
  post_init: |
    set -e
    echo "post_init: generating deploy/bindings.gen.txt"
    printf 'generated-by: template-v1.3.0\n' > deploy/bindings.gen.txt
  post_update: |
    set -e
    echo "post_update: regenerating bindings (v1.3.0)"
    printf 'generated-by: template-v1.3.0\n' > deploy/bindings.gen.txt
    printf 'build %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > deploy/.buildinfo
```

(The block above is the hook actually used by this document's demo template v1.3.0; `deploy/.buildinfo` deliberately writes a file that is not in the plan, to demonstrate how "unplanned side effects" are reported.)

A block scalar, **handed whole to one `sh -c` executed at the project root** (not line by line — `cd`, heredocs and `set -e` all rely on this). Hooks are arbitrary shell commands supplied by the upstream template and require your trust confirmation before execution; see [Hooks and trust](#10-hooks-and-trust).

### `upstream.paths`

Declares the **shared surface**: only template paths matching these globs may be sent back by a derived project via `backflow` (matching the **original in-template path**, same semantics as `exclude`; a leading `!` excludes):

```yaml
upstream:
  paths: ["internal/util/**", "docs/adr/**"]
```

- Domain checks use the **path mapped back into the template** (the inverse of `rename` / path variables); patterns given via the `--paths` flag are **unioned** with these, and the report notes each line's source.
- When the manifest has no such key and no `--paths` was given, `backflow` fails with exit 1 (rather than silently judging "everything is outside the shared surface", which would look like the commit was wrong).
- **Compatibility cost**: once a template declares `upstream`, every derived project must upgrade to an xinit that supports the key in order to `init` / `update` / `adopt` (manifest parsing is strict — undeclared top-level keys fail immediately); read-only commands (`check` / `status`) are unaffected.

### Other keys

The keys below are not used by this document's demo template but are part of the manifest schema:

| Key | Meaning |
| --- | --- |
| `exclude.paths` | Whole-tree skip on match (matched against the **original in-template path**), e.g. `["dist/**", "node_modules/**"]` |
| `upstream.paths` | Shared-surface globs: only these paths may come back (see above), e.g. `["internal/util/**"]` |
| `rename[]` | `{from, to}`, substring replacement applied to every path segment (same letter-boundary matching as `substitute`; substring replacement, not whole-segment equality) |
| `protected_literals` | `[]string`, document-scoped literal protection zones: content inside them is never rewritten by `substitute` |
| `definitions` | Exists only to host YAML anchors; ignored after parsing (strict mode requires top-level keys to be declared, so anchor variables live here) |

### Full example (the v1.0.0 manifest of this document's demo template)

```yaml
manifest_version: 1

vars:
  - name: project_name
    prompt: "Project display name (Go-identifier style)"
    default: "SvcKit"
  - name: app_slug
    default: "{{ .project_name | lower }}"
    pattern: '^[a-z][a-z0-9-]*$'

substitute:
  - from: "SvcKit"
    to: "{{ .project_name }}"
    paths: ["**/*.go", "**/*.yaml"]
  - from: "svckit"
    to: "{{ .app_slug }}"
    paths: ["go.mod", "Taskfile.yml", "deploy/**"]

render:
  - paths: ["**/*.tmpl"]
    strip_suffix: ".tmpl"

generated:
  paths: ["deploy/bindings.gen.txt"]
```

### Path glob rules

- A pattern **without `/`** matches at any depth with gitignore semantics (`*.go` matches `a/b/x.go`); a pattern **with `/`** is anchored at the template root (`build/**` does not match `frontend/build/x`).
- A leading `!` excludes, in order; once a directory is excluded, a deeper `!` cannot re-include it.
- Matching is **case-sensitive** and compares byte-wise.
- Whatever the manifest says, `.git/`, `.xinit/` and `xinit.yml` never enter derived projects and never participate in the version anchor.

## 6. Conflicts and convergence

**When conflicts happen**: the template and you both changed the same file, at the same spot (near the same line). Changes to different files, or non-adjacent changes in the same file, are merged directly by `update`.

Behavior on conflict (exit code **2**). Below is a real run (template repository `$WORK/my-service-template`, conflicting file `docs/notes.md`):

```
$ xinit update
xinit update: /path/to/my-service-template
Version: v1.1.0 → v1.2.0
(still unresolved this round: the version recorded in state stays v1.1.0 — converge and re-run to advance; §10.2 step 9)
  merge-conflict       docs/notes.md
Summary: writes 0, deletes 0, chmod 0, conflicts 1
Conflicts (1) need your attention:
  conflict             docs/notes.md
  accept?              xinit update --accept 'docs/notes.md'
Option 1: resolve the conflict by hand so the content matches the template (or wait for the template to revert), then re-run update to converge;
        if the resolution differs from the template, declare it resolved with the xinit update --accept <path> above (records the version you saw and approved as base, §10.3).
Option 2: write the path into .xinit/prune.txt to take it out of management (the template's changes to it are no longer synced).
Reference: the template's new content was written to .xinit/conflicts/<path>.
```

Type conflicts (symlink / directory obstacles) cannot be `--accept`ed (§10.3) — when a section is entirely type conflicts, "Option 1" is replaced with "type conflicts have no `--accept` way out — write the path into `.xinit/prune.txt` (leave management), or move the obstacle aside".

Key points:

- **The disk is never modified**; the template version of a conflicting path is written to `.xinit/conflicts/<path>` for you to merge by hand.
- **The recorded version does not advance** (it stays the old version), so re-running `update` after convergence automatically selects the same new tag again.
- The presence or absence of `.xinit/conflicts/` **is** "are there unresolved conflicts": entries that are resolvable and no longer conflicting get cleaned up, and the directory disappears once all are resolved.
- **On a real terminal it asks one by one** (same under a pipe with `--interactive`): `<path> (<conflict type>) switch to the template version? [y/N] ` — answering `y` = overwrite local with the template version (your previous content is backed up to `.xinit/backup/` first, and the path converges this round); **Enter alone = keep local**, which is the status-quo behavior above (single-key exit). See [Interactive prompts](#interactive-prompts).

**Two ways out:**

1. **Merge by hand**, then choose one:
   - if the resolution happens to match the template → just re-run `xinit update` to converge;
   - if the resolution is a merge of "your version + the template's intent" → the re-run must carry `--accept`:

     ```bash
     xinit update --accept docs/notes.md      # multiple paths allowed
     ```

     Semantics: **record the template version you last saw and approved as the new base** (i.e. the content of `conflicts/<path>`, not the template from this run), then merge as usual. This is not "blindly trusting the template" — it only declares "I did see and handle this conflict". If the path still differs from the template after convergence, `status` will list it as a local change; that is normal.
     `--accept` cannot be combined with `--dry-run`; the paths must actually be in `.xinit/conflicts/`; paths already `generated` and skipped, or already written into `prune.txt`, are rejected with an error.
2. **Write it into `.xinit/prune.txt`** (one path per line): the path **leaves management** — from then on it is your ordinary file, the template's changes to it are no longer synced, and it no longer produces conflicts.

After converging, one more run should be all `Noop`:

```
$ xinit update
Version: v1.2.0 → v1.2.0
Summary: writes 0, deletes 0, chmod 0, conflicts 0
```

## 7. The `.xinit/` directory

The `.xinit/` directory at a derived project's root is the state directory and **never goes into version control** (`init`/`update` idempotently write it into `.git/info/exclude`; `status` and `check` only report and never write).

| Entry | Role |
| --- | --- |
| `state.json` | The managed-path inventory (each entry has `src`, content hash, exec bit, binary/mixed-EOL marker, `generated` marker), template source and version, variable values, trusted hooks |
| `state.json.prev` | Backup of the previous `state.json` (old bytes are kept before every save; the first write produces none). When `state.json` is corrupted, follow the error's guidance to recover by hand — copy it back over `state.json` and retry; **no automatic rollback** (`Load` does not read it) |
| `base/` | The rendered **baseline tree** (a plain directory copy) — the base side of the three-way comparison |
| `conflicts/` | Unresolved conflicting paths → template versions; the directory's existence means "conflicts to handle" |
| `overwritten/` | Your content overwritten by the template (`generated` paths), kept persistently |
| `backup/` | Previous content + exec bit before each `update` rewrite, one directory per run start time (`-2`, `-3` appended on collisions), **never overwritten, never auto-pruned** (prune old copies: `xinit backup prune --keep N`) |
| `backflow/` | Backflow output (`.patch` files produced by `backflow`; filenames carry both the derived and template short shas). Its presence inside `.xinit/` is already covered by the whole-directory `.git/info/exclude` entry — no separate registration |
| `lock` | Project lock preventing concurrent `update` (`diff` / `status` / `check` are read-only and take no lock); flock on unix, LockFileEx on Windows; the lock file persists and is never deleted; on Windows it is held in shared mode while locked and cannot be deleted by external tools (§22.3) |
| `prune.txt` | One path per line: these paths leave management and are no longer synced (one-way, see below) |

`prune.txt` is **one-way**: on the next (effective) `update` round, a listed path is removed from both `.xinit/base/` and the `state.json` records — that round gives no separate notice that these paths left management (nor does the summary count show it). From then on `xinit restore` does not apply to them (they are no longer recorded), and the template's changes to them are completely silent. To bring one back under management: delete its line from `prune.txt` and run `update` again — the path is re-adopted as a "template addition": if the file is absent locally, the template's recorded version is written; if a same-named file exists locally, an exact match is adopted directly, otherwise a `conflict-both-added` conflict is reported (exit 2, the template version is written to `.xinit/conflicts/`, your file untouched) — handle it by hand until it matches the template and re-run, or declare it resolved with `--accept` per §6 (recording the template version from `conflicts/` as base; local content is treated as a local change from then on).

### User cache directory and render snapshot cache

After `init` / `adopt` succeed, and after an `update` **convergence round** succeeds (no unresolved conflicts, fold rejections or blockers; all of these after the project state has been persisted), this round's **rendered originals** are also written to the user-level cache directory (best-effort: a write failure only warns on stderr; the command still succeeds with zero impact on project state; §21). It is used **only** as a recovery source when the project's `.xinit/base/` (baseline tree) is lost or corrupted — covering the two gaps "dir-source base lost" and "the git-source recorded commit is no longer fetchable from the template source" that previously required `adopt` (or a hard error); the **recovery chain** does not read snapshots while base is healthy (zero hits also create no cache root).

- **Location**: `<UserCacheDir>/xinit/render/<familyKey>/` — `~/Library/Caches/xinit/render/` on darwin, `$XDG_CACHE_HOME/xinit/render/` on Linux, `%LocalAppData%\xinit\render\` on Windows; each entry holds `manifest.json` (the manifest) and `tree/<Dst>` (per-path rendered originals). **Windows note**: when `%LocalAppData%` is undefined (service accounts / trimmed CI), user-cache directory resolution fails — the snapshot write side degrades to a one-line warning (best-effort), and the read side (snapshots/mirrors) is handled through its existing error surface (§22.14, §22.17 R14-12).
- **Deletable wholesale**: `rm -rf <cacheRoot>/render` (`<cacheRoot>` = `<UserCacheDir>/xinit`) — afterwards the behavior equals having no snapshots, which is also the repair guidance for corrupted snapshots.
- **No GC**: each family (= one "input tuple": engine version, template source, kind, vars) holds at most one entry, rotated by whole-entry overwrite; the total equals the number of deduplicated input tuples, growing slowly with use, **never auto-pruned**.
- **Known limitation (cross-project same-family boundary)**: two projects sharing (engine, source, kind, vars) but pinned to different commits — the later writer overwrites the earlier entry (last-manifest-wins); the earlier project can then only fall back to git re-rendering / a dir hard error if its base is lost.
- **After an engine upgrade**: old entries go stale naturally (a failed check is a silent miss) and can be deleted by hand.

## 8. Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, nothing pending |
| 1 | Error |
| 2 | Conflicts, fold rejections or blockers to resolve |
| 3 | Hook failure (files and state were persisted correctly) |
| 4 | Trust confirmation required |
| 5 | `diff` found non-`Noop` actions; or `status --fetch` found available updates |
| 6 | A check found problems (`xinit check` has `FAIL` items) |

`diff` returns 0 when there are no actions; `status` without `--fetch` always returns 0 (unless an error occurs); `check` returns 0 when there are only `warn` / `skip`; `generated` paths skipped under `--no-hooks` need no action from you and **do not affect the exit code**.

`backflow` **uses only 0 / 1 (no new codes)**: 0 = exported successfully (**including with warnings**); 1 = error (eligibility, not converged, all paths filtered by the escape hatches, all noop, IO). "Has warnings" is not 2 — 2 is `update`'s "pending conflicts" semantics, which backflow does not have (conflicts always fail closed with exit 1).

`restore` **uses only 0 / 1 (no new codes)**: 0 = everything restored or unchanged; 1 = error (including refused overwrites — a failed preflight means zero writes).

`backup prune` **uses only 0 / 1 (no new codes)**: 0 = done (including when nothing is deleted); 1 = error (`--keep < 1`, not an xinit project — both with zero disk actions; and deletion failures).

`template` and `changelog` **use only 0 / 1 (no new codes)**: 0 = the report was printed (including "nothing to list"); 1 = error (`--revision` without `--changelog`, `--changelog` on a dir source, `--changelog` with a missing mirror, a nonexistent target version, a template with no stable tags and no `--revision`, a recorded commit absent from the template history, an unknown `changelog` version). "Updates available" is not 5 — 5 is exclusively `status --fetch` semantics; to test for updates in scripts use `status --fetch`.

Interactive answers introduce no new exit codes: aborting vars collection (EOF) = 1, the default conflict answer = the status quo (2), switching to the template version = the normal write path (convergence round 0), trust refusal = 4.

## 9. Backflow

`backflow` is the third step of the lifecycle — **instantiate (`init`) → update (`update`) → backflow (`backflow`)**: it **structurally** translates a derived project's change to the **shared surface** (manifest `upstream.paths` ∪ `--paths`) back into the template's coordinate system, producing a patch the template author can `git am` directly.

This is an **opt-in** capability: `init` / `update` do not require the derived project to be a git repository, `backflow` does (it uses git to enumerate commits and read their content — see 3.2). Everything else behaves exactly as before when this command is not used.

```
derived side   xinit backflow <commit>     → .xinit/backflow/<derived-sha7>-<template-sha7>-<slug>.patch
transfer       hand the patch to the template author via any channel
receive        git am -3 [--keep-cr] <patch> + template-side check/tests + CHANGELOG + tag   ← xinit takes no part in receiving
broadcast      all derived projects pick the result up with xinit update (round trip closes = next update has zero conflicts)
```

The target commit must pass these preconditions (any failure is reported, exit 1): the derivation is a git repository; no operation in progress (merge / rebase / cherry-pick / revert); **tracked files are clean** (untracked files only warn, never block); the commit exists and is neither root nor a merge; the commit message's **final trailer block** contains the verbatim line `Template-Upstream: yes`; the commit has changes; `.xinit/base` is **of the same origin** as the recorded template commit (a mixed state after a previous round's `pending` base advance must first converge with `xinit update`); the changes fall inside the shared surface.

**Translation and smuggling scan**: changes in the pre-image region (the part where both the template original and base exist) are structurally restored to **template wording** by a three-way merge; **added lines / added files are not translated** (no pre-image, no translation) — they are instead flagged one by one by the **smuggling scan** (`backflow-warn <file>:<line>: <token> — <content>`; for added files whose path segments match, `path: token`), for the template author to rewrite by hand. When a change lands near a substitution point (same hunk neighborhood) it is a **translation conflict**: the whole run fails closed, no patch is produced, and the reported conflict blocks have three sides — the `ours` side = **template wording** (reference side), the `base` side = the rendered baseline (reconstructed from `.xinit/base`), the `theirs` side = your commit's content; rewrite the conflict lines back to template wording and re-export.

**Receiving guide** (printed verbatim in `backflow`'s report on success; the template author does **not** need xinit installed):

1. **The receiving repository must contain the history of the recorded template commit** — it is the patch's pre-image; force-pushes / rewritten history break `-3`'s index fallback. Recommended: `git checkout -b backflow/<derived-sha7>` first.
2. **CRLF templates must use `git am -3 --keep-cr`** (or `git config am.keepcr true` in the receiving repository): plain `am` strips CRs from the patch's content lines, failing hard with rc=128 and leaving an interrupted state.
3. The receiving repository's working tree must be clean (`am` rewrites it directly).
4. The patch is produced with `--full-index` (40-char blobs), so the receiving git must be ≥ 2.38 (the same as xinit's dependency).

Apply with `git am -3 <patch>` (add `--keep-cr` for CRLF templates). Interrupted state: `git am --abort` to give up; after resolving conflicts, `git am --continue`. It is fine if the template HEAD has moved on — `-3` rebuilds the baseline from blob indices and the commit lands on top of the current HEAD; only when the target commit is no longer in history does `am` fail and leave an interrupted state — `--abort` and retry with `git am -3`, or port by hand.

**Known limitations** (M8):

- **The conflict radius is the hunk neighborhood of a substitution point** (the token line ±1, and insertions/deletions adjacent to the token line), larger than the intuitive "same line" — substitution points often cluster in a file (title / copyright header / imports / config names).
- `render`-mode files, deletions / renames / copies, binaries and mixed line endings, symlinks, `generated`-flagged files, pruned paths and paths whose template origin has disappeared: all are **warned about, not exported**.
- Multiple derivations sending back same-named new files are not deduplicated: on receipt the second derivation's patch conflicts, and the template author decides the merge.
- The smuggling scan has **no hard gate** (xinit has no template-side checker): it is warning-level plus template-author review, and the report says so itself.
- Output filenames carry the derived and template short shas: re-exporting after the template ships a new version is a **new artifact** and does not overwrite the old one; `backflow` never auto-cleans `.xinit/backflow/` (same rule as `backup/`).
- Shares git's mailbox boundaries: a standalone `---` line in a commit message, and lines shaped like `From … Mon Sep 17 00:00:00 2001` (especially as the last line), are silently truncated by mailinfo.
- Boundaries: template repos using the sha256 object format are not supported for `-3` fallback; a purely non-ASCII commit subject degrades the filename slug to `patch` (the filename stays unique via its two short shas).

## 10. Hooks and trust

The manifest's `hooks` are **arbitrary shell commands from the upstream repository**, so they must be confirmed by you before execution.

- **When confirmation happens**: on first encountering a set of commands, or when the content changed since last time. The tool prints the **full hook text** (plus a line-level diff when the content changed), then asks for `--yes` or an interactive `y`. The diff's baseline is the **currently recorded template version**, so right after skipping a round with `--no-hooks`, the next confirmation may have no diff section (the version has already advanced to the one containing the new hook).
- **Refusal costs nothing**: without `--yes`, ending with exit code **4** makes **no modification this round** (no files written, no backups, no state touched). Confirmation always happens before any write.
- **`--yes` bypasses the trust gate once**: when enabling it in automation (CI), **the template source itself is the trust root** — it must come from a trusted channel only.
- **`--no-hooks`**: skips execution but still records this run's content — so the next run without `--no-hooks` **still needs confirmation** (skipping is not trusting). In this mode:
  - `generated` paths are **skipped entirely** (no conflict judgment, disk untouched, base keeps the old value), and are handled normally as "template-generated artifacts" when re-run with hooks; this does not affect the exit code.
- **Hook failure**: exit code **3**. Files and state are **both persisted correctly and intact** (the rendered output itself is fine), and the tool prints the failed command and how to re-run it by hand.
- **Side-effect report**: a directory snapshot is taken before and after hook execution, and the report lists
  - "unplanned side effects" — paths the hook touched that were not in the plan (e.g. files written by `npm install`);
  - "in-plan but rewritten by the hook" — these paths may be judged "locally changed" next round, and the report names them.
- **When hooks run**: `post_init` after `init` persists; `post_update` after **every** `update` round persists — **including `Noop` rounds with no template changes**. Hooks should therefore be idempotent, and should be written knowing they run every round.
- **No rollback promise**: hooks are arbitrary commands — `npm install` rewrites dependency directories, generators may delete old files that were not in the plan, and "the working tree byte-identical to before the run" is physically impossible. What the tool guarantees is: originals of **in-plan paths this project rewrote** are all in `.xinit/backup/<timestamp>/`; anything a hook broke is yours to restore (the template side has git too).
- **Platforms**: hooks run as `sh -c` (§22.12 keeps this the only form) — platforms without a POSIX `sh` on PATH do not support hooks and fail with a clear error (on Windows, Git for Windows' bundled sh satisfies this).

## 11. Known limitations

1. **Platforms**: macOS / Linux / **native Windows** (§22 design; the Windows on-device verification checklist is §22.17 — this batch includes cross-compilation gates and the proxy semantics testable on darwin; on-device behavior is not verified). Hooks run as `sh -c` (on Windows via Git for Windows' sh); Windows has no "executable bit" semantics: exec recording works as usual, disk comparison is skipped per platform (§22.6), and `check`'s A9 is skipped per platform; while `.xinit/lock` is held (Windows handle share mode) external tools cannot delete it; reparse/junction intermediate segments are explicitly rejected, full semantics unsupported (§22.5, §22.16 P6).
2. **Version selection for git templates (important)**: `init` takes the template's default-branch **HEAD** by default and records it as `"HEAD"` — and `"HEAD"` cannot take part in semver comparison, so **the first `update` after `init` must explicitly pass `--revision <tag>`** (otherwise exit 1, with a hint in the error message). After one alignment to a `vX.Y.Z`-form tag, the default mode works. **Prefer `init --revision <vX.Y.Z-form tag>` to pin the version directly** (a prerelease suffix is fine too, provided the template also has at least one stable `vX.Y.Z` tag — default selection only picks among stable tags): the state records that tag, and `update`'s default mode works immediately, no alignment first. A branch name / commit sha / non-`vX.Y.Z` tag (e.g. `1.0.0`, `release-1`) passed to `--revision` follows the same rule as the default `"HEAD"` (what's recorded is the reference's original text, not semver, so the first `update` still needs an explicit `--revision`). (A dir source has no such issue: it has no version concept, and the first `update` just runs — see §4.)
   Recommendation: to ship a version of a template repository, **tag it first, then create new projects with `init --revision <latest tag>`**. Without the flag, when the template's HEAD is ahead of the latest tag, `init` carries those unreleased changes into the new project; if you then align to the latest tag, those changes are **reverted** (the tool prints a "the target version is not a descendant of the current version (downgrade/rollback)" warning but does not stop you). Pinning to the tag avoids carrying unreleased changes.
3. **No rollback promise**: see "No rollback promise" in the previous section.
4. **Automatic migration when templates change paths, with fallbacks**: when the template physically renames `a.go` to `b.go` (content unchanged), or changes a variable that appears in paths (e.g. `app_slug`), or when `rename` rules move files, `update` **carries local changes on old paths over to the new path** and treats the old path as deleted (its content goes to `.xinit/backup/` as usual). The report's "path migration" section gives each pair one of four states (identical judgment and wording under `diff` / `--dry-run` and real runs):
   - `migrated`: local changes were preserved through the migration, the old path is treated as deleted;
   - `rename`: no local changes; an ordinary delete + add performed the rename;
   - `skip`: migration skipped, behavior as if not migrating (the old path keeps its local version with a warning, the new path is treated as a template addition), with the reason in parentheses (generated path, empty-content pairing, type mismatch, both name and directory changed with insufficient pairing confidence, or the new-path write / old-path delete being refused);
   - `fallback`: migration not completed (both template and local changed and they conflict / binary or mixed line endings excluded from the three-way merge / the three-way merge failed) — the old path **keeps the local version**, the new path gets the template version, and **you must merge the local changes into the new path by hand, or write the old path into `.xinit/prune.txt`**.

   **Retry semantics**: after a `fallback`, and after non-refusal `skip`s, the pair is **not retried automatically**; for refusal-type `skip`s it depends on which side refused — when the refusal lands on the **new path** (e.g. a same-named directory already on disk, a case-fold collision), **the new path is not written this round (exit code 2)**, and re-running after moving the obstacle pairs and completes the migration again; when it lands on the **old path** (a delete-path resolution escape), the new path is written with the template version as usual and the pair is never retried, handled per the `fallback` manual route. Unrecognized cases still go through delete + add (locally changed old paths are kept with a warning; no migration line in the report): a template that **renames and changes content at the same time**, and ambiguous pairings where multiple old paths and new paths cannot be matched one-to-one.
5. **`git ≥ 2.38` is a hard dependency**: checked at startup, exit if unmet (`changelog` does not touch git and skips the check).
6. **Projects initialized with pre-M9 versions**: if the template source was given as a relative path from outside the project directory (a relative path that cannot resolve inside the project directory), `state.template.source` recorded that relative path — running `update` inside the project directory then falls into the git-URL branch and fails (historical behavior). Change `template.source` in `.xinit/state.json` to an absolute path to recover; since M9, `init`/`adopt` always record absolute paths for local path sources.
7. **Template source directory moved/renamed**: `state.template.source` records an absolute path — after moving the template directory, change `template.source` in `.xinit/state.json` to the new absolute path to recover.
8. **Path character tightening (since M11, §22.4)**: no segment of a managed path may contain `\` or `:` (separator / drive-letter and ADS semantics on Windows). Dir-source files that used to be renderable (e.g. `a\b.txt`, `a:b.txt`) are explicitly rejected since M11 — rename them, or switch to a git template source; such filenames cannot exist in git sources, which are unaffected. Also, path-validation error wording was unified with the single-point predicates (e.g. "must not be an absolute path" → "is an absolute path: ..."); the criteria are unchanged — mind any scripts comparing these messages.
9. **Dir-source version anchors include the exec bit (§22.7 R4c)**: the dir-source tree hash includes executable-bit information — after copying a template directory across platforms (e.g. macOS → Windows) exec recording may flip, and `update` may report "template version changed"-type output while the template content is actually unchanged (ignore it, or switch to a git source).
10. **Permission relaxation on failed write paths (§22.8, an explicit trade-off)**: on unix, when a target file is read-only and its directory is not writable, the atomic write (`update` persistence) clears the file's read-only bit before the rename fails and does **not** restore it after the failure — i.e. on the failure path the file may become writable (the success path is unaffected: "the clear targets the destination's bits" is §22.8's trade-off, not a defect). If you notice permission changes after such a failure, just `chmod` again as needed.

## 12. Development

```bash
make check     # fmt + vet + test — run this before committing
```

| Target | Effect |
| --- | --- |
| `make build` | `go build -o bin/xinit .` |
| `make install` | `go install .` |
| `make test` | `go test ./... -count=1` |
| `make vet` | `go vet ./...` |
| `make fmt` | fails when `gofmt -l .` prints anything |
| `make check` | `fmt` + `vet` + `test` |
| `make clean` | remove `bin/` |

Test structure:

- **Unit tests** live in the `internal/*` packages (rendering, manifest, decision table, merge, state, template acquisition, …).
- **End-to-end tests** live in `internal/cli/testdata/script/*.txtar` (71 scripts, run by `go test ./internal/cli`). Each script is a real command sequence exercising the full flows of `init`/`update`/`diff`/`status`/`template`/`check`/`adopt`/`restore`/`backflow`/`backup prune` in a temp directory (the `changelog` scripts create no project and only verify read-only output), including conflicts, hook trust, generated files, backups, path migration, backflow round-trips and exit-code assertions; scripts invoke the same `Execute()` entry point through two commands, `xinit` and `xinit-code`; `xinit-code` prints the exit code as `exit=N` for assertions.
