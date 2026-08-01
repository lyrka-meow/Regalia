#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
repo=${REGALIA_GITHUB_REPO:-lyrka-meow/Regalia}
tag=${REGALIA_ROLLING_TAG:-rolling}
version=${1:?usage: publish-rolling-release.sh VERSION [ARCH]}
arch=${2:-x86_64}

command -v gh >/dev/null 2>&1 || {
    echo "GitHub CLI (gh) is required." >&2
    exit 1
}
gh auth status >/dev/null
cd "$repo_root"
[[ -z $(git status --short --untracked-files=no) ]] || {
    echo "Commit tracked changes before publishing the rolling release." >&2
    exit 1
}

archive=$("$repo_root/packaging/github/build-binary-release.sh" \
    "$version" "$arch")
checksum="$archive.sha256"
commit_sha=$(git rev-parse HEAD)

if ! gh release view "$tag" --repo "$repo" >/dev/null 2>&1; then
    gh release create "$tag" \
        --repo "$repo" \
        --target "$commit_sha" \
        --title "Regalia Rolling — $version" \
        --notes "Rolling binary build $version from commit $commit_sha. This single release is updated in place." \
        --prerelease
fi

gh release upload "$tag" "$archive" "$checksum" \
    --repo "$repo" \
    --clobber

new_archive=$(basename "$archive")
mapfile -t assets < <(
    gh release view "$tag" --repo "$repo" --json assets --jq '.assets[].name'
)
for asset in "${assets[@]}"; do
    case "$asset" in
        regalia-"$version"-*.tar.zst|regalia-"$version"-*.tar.zst.sha256) ;;
        *) gh release delete-asset "$tag" "$asset" --repo "$repo" --yes ;;
    esac
done

gh api --method PATCH "repos/$repo/git/refs/tags/$tag" \
    -f sha="$commit_sha" -F force=true >/dev/null
gh release edit "$tag" \
    --repo "$repo" \
    --target "$commit_sha" \
    --title "Regalia Rolling — $version" \
    --notes "Rolling binary build $version from commit $commit_sha. This single release is updated in place." \
    --prerelease >/dev/null

printf 'Updated https://github.com/%s/releases/tag/%s with %s\n' \
    "$repo" "$tag" "$new_archive"
