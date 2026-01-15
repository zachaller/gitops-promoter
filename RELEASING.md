# Release Process

This document describes the release process for gitops-promoter, including the release branch strategy for managing patch releases.

## Release Branch Strategy

The project uses a release branch strategy to support patch releases and security fixes:

- **Main branch (`main`)**: Active development for the next minor or major release
- **Release branches (`release/x.y`)**: Maintenance branches for patch releases
  - Example: `release/0.20`, `release/0.21`
  - Created automatically when a minor release (x.y.0) is tagged
  - Used for cherry-picking bug fixes and creating patch releases

## Release Types

### Minor Release (x.y.0)

Minor releases are cut from the `main` branch and introduce new features or significant changes.

**Process:**

1. **Trigger the bump-docs workflow:**
   - Go to Actions → "Bump Docs Manifests"
   - Click "Run workflow"
   - Enter the new version (e.g., `0.21.0`)
   - Leave target branch as `main` (default)
   - This creates a PR to update documentation with the new version

2. **Review and merge the PR:**
   - Review the changes in the PR
   - Merge the PR to `main`

3. **Automatic release:**
   - When the PR is merged, the release workflow automatically triggers
   - Creates a git tag (e.g., `v0.21.0`)
   - Builds binaries, Docker images, and manifests using GoReleaser
   - Creates a draft GitHub release with changelog
   - **Automatically creates a release branch** (`release/0.21`)

4. **Finalize the release:**
   - Review the draft GitHub release
   - Edit release notes if needed
   - Publish the release

### Patch Release (x.y.z where z > 0)

Patch releases are cut from release branches and contain only bug fixes, security patches, or critical updates.

**Process:**

1. **Cherry-pick fixes to the release branch:**

   Choose one of the following methods:

   **Option A: Automated cherry-pick (recommended)**
   - Go to Actions → "Cherry-pick to Release Branch"
   - Click "Run workflow"
   - Enter the commit SHA from `main` that you want to cherry-pick
   - Enter the target release branch (e.g., `release/0.20`)
   - The workflow will:
     - Attempt to cherry-pick the commit
     - Create a PR if successful
     - Report conflicts if manual resolution is needed

   **Option B: Manual cherry-pick**
   ```bash
   # Checkout the release branch
   git checkout release/0.20
   git pull

   # Create a feature branch
   git checkout -b fix/backport-issue-123

   # Cherry-pick the commit(s)
   git cherry-pick <commit-sha>

   # If conflicts occur, resolve them and continue
   git add .
   git cherry-pick --continue

   # Push and create a PR
   git push origin fix/backport-issue-123
   ```

2. **Review and merge the cherry-pick PR:**
   - Ensure the fix is appropriate for the release branch
   - Verify tests pass
   - Merge the PR to the release branch

3. **Trigger the bump-docs workflow:**
   - Go to Actions → "Bump Docs Manifests"
   - Click "Run workflow"
   - Enter the new patch version (e.g., `0.20.3`)
   - **Important:** Set target branch to the release branch (e.g., `release/0.20`)
   - This creates a PR against the release branch

4. **Review and merge the PR:**
   - Merge the PR to the release branch

5. **Automatic release:**
   - When the PR is merged, the release workflow automatically triggers
   - Creates a git tag (e.g., `v0.20.3`)
   - Builds and publishes the patch release

6. **Finalize the release:**
   - Review and publish the draft GitHub release

## Release Branch Lifecycle

### Creation

Release branches are **automatically created** by the `create-release-branch` workflow when a minor release tag (x.y.0) is pushed:

- Trigger: Tag matching pattern `v[0-9]+.[0-9]+.0`
- Creates branch: `release/x.y` from the tag
- Example: Tag `v0.21.0` creates branch `release/0.21`

### Maintenance

Once created, release branches:

- Accept cherry-picks of bug fixes from `main`
- Follow the same PR and review process as `main`
- Should only contain changes necessary for patch releases
- Avoid feature work or refactoring

### Branch Protection

Release branches should be protected with the same rules as `main`:

- Require PR reviews
- Require status checks to pass
- Prevent force pushes
- Prevent deletion

## Cherry-pick Guidelines

When deciding what to cherry-pick to a release branch:

**DO cherry-pick:**
- Critical bug fixes
- Security patches
- Regression fixes
- Documentation updates for the release version
- Dependency updates for security vulnerabilities

**DON'T cherry-pick:**
- New features
- Breaking changes
- Refactoring or code cleanup
- Non-critical dependency updates
- Changes that don't affect the release version

## Workflows

### Bump Docs Manifests

**File:** `.github/workflows/bump-docs-manifests.yml`

Manually triggered workflow to update version references in documentation.

**Inputs:**
- `new_version`: Version number (e.g., `1.2.3`)
- `target_branch`: Target branch (`main` or `release/x.y`)

**Actions:**
- Runs `hack/bump-docs-manifests.sh` to update version references
- Creates a PR with commit message: `docs: bump manifest versions to vX.Y.Z`

### Release

**File:** `.github/workflows/release.yaml`

Automatically triggered when a PR with version bump is merged.

**Triggers:**
- Push to `main` or `release/**` branches
- Commit message contains: `docs: bump manifest versions to v`
- Changed files: `docs/getting-started.md` or `docs/tutorial-argocd-apps.md`

**Actions:**
- Extracts version from commit message
- Creates and pushes git tag
- Runs GoReleaser to build and publish release

### Create Release Branch

**File:** `.github/workflows/create-release-branch.yaml`

Automatically triggered when a minor release tag is created.

**Triggers:**
- Tag matching pattern: `v[0-9]+.[0-9]+.0`

**Actions:**
- Extracts major.minor version from tag
- Creates release branch: `release/x.y`
- Pushes branch to repository

### Cherry-pick

**File:** `.github/workflows/cherry-pick.yaml`

Manually triggered workflow to cherry-pick commits to release branches.

**Inputs:**
- `commit_sha`: Commit SHA to cherry-pick
- `target_branch`: Target release branch (e.g., `release/0.20`)

**Actions:**
- Validates inputs and target branch
- Creates a feature branch
- Attempts to cherry-pick the commit
- Creates a PR if successful
- Reports conflicts if manual resolution needed

## Troubleshooting

### Cherry-pick Conflicts

If the automated cherry-pick workflow reports conflicts:

1. Check the workflow summary for conflicted files
2. Follow the manual cherry-pick instructions provided
3. Resolve conflicts locally
4. Push the resolved branch and create a PR

### Release Branch Not Created

If a release branch wasn't automatically created:

1. Check the "Create Release Branch" workflow run for errors
2. Manually create the branch if needed:
   ```bash
   git checkout v0.21.0  # the release tag
   git checkout -b release/0.21
   git push origin release/0.21
   ```

### Release Workflow Not Triggering

If the release workflow doesn't trigger after merging the bump-docs PR:

1. Verify the commit message contains: `docs: bump manifest versions to v`
2. Check that one of the documentation files was modified:
   - `docs/getting-started.md`
   - `docs/tutorial-argocd-apps.md`
3. Verify the target branch is `main` or `release/**`

## Examples

### Example 1: Cutting a new minor release

```bash
# 1. Trigger bump-docs workflow
#    Actions → Bump Docs Manifests → Run workflow
#    new_version: 0.22.0
#    target_branch: main

# 2. Review and merge the PR

# 3. Automatic actions happen:
#    - Release workflow creates tag v0.22.0
#    - GoReleaser builds and publishes
#    - Create Release Branch workflow creates release/0.22

# 4. Publish the draft release on GitHub
```

### Example 2: Creating a patch release

```bash
# 1. Cherry-pick a fix (assuming commit abc123 fixes a bug)
#    Actions → Cherry-pick to Release Branch → Run workflow
#    commit_sha: abc123
#    target_branch: release/0.22

# 2. Review and merge the cherry-pick PR

# 3. Trigger bump-docs workflow
#    Actions → Bump Docs Manifests → Run workflow
#    new_version: 0.22.1
#    target_branch: release/0.22

# 4. Review and merge the PR

# 5. Release workflow automatically creates v0.22.1

# 6. Publish the draft release on GitHub
```

### Example 3: Manual cherry-pick

```bash
# Cherry-pick multiple commits to release/0.22
git checkout release/0.22
git pull
git checkout -b backport-fixes-123-456

# Cherry-pick commits
git cherry-pick abc123
git cherry-pick def456

# Push and create PR
git push origin backport-fixes-123-456
# Create PR via GitHub UI targeting release/0.22
```

## Version Numbering

The project follows [Semantic Versioning](https://semver.org/):

- **Major (x.0.0)**: Breaking changes, incompatible API changes
- **Minor (x.y.0)**: New features, backwards-compatible
- **Patch (x.y.z)**: Bug fixes, security patches, backwards-compatible

Version numbers are embedded at build time via GoReleaser ldflags.
