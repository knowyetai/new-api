#!/usr/bin/env bash
# Run locally with a commit already pushed to GitHub:
#   bash deploy/production/deploy.sh <commit-hash> [user@host] [deployment-directory]
# Defaults: root@8.145.51.132 /opt/knowyet-model-gateway
# 从服务器的 new-api 仓库获取指定提交；失败后由人传入旧 hash 重新部署。
set -Eeuo pipefail

# 阶段 1：解析指定提交和部署目标。本地未提交内容不会参与部署。
if [[ ${1:-} == --help ]]; then
  sed -n '2,5p' "${BASH_SOURCE[0]}"
  exit 0
fi
if (( $# < 1 || $# > 3 )); then
  echo 'Usage: bash deploy/production/deploy.sh <commit-hash> [user@host] [deployment-directory]' >&2
  exit 1
fi

requested_revision=$1
remote_host=${2:-root@8.145.51.132}
deploy_dir=${3:-/opt/knowyet-model-gateway}
[[ $requested_revision =~ ^[a-fA-F0-9]{7,40}$ ]] || { echo 'Provide a commit hash (7-40 hexadecimal characters), not a branch or tag' >&2; exit 1; }
[[ $remote_host =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.@-]*$ ]] || { echo 'Invalid SSH target' >&2; exit 1; }
[[ $deploy_dir =~ ^/[a-zA-Z0-9_/-]+$ && $deploy_dir != / ]] || { echo 'Invalid deployment directory' >&2; exit 1; }

for command in git ssh; do
  command -v "$command" >/dev/null || { echo "Missing local command: $command" >&2; exit 1; }
done
repo_dir=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
revision=$(git -C "$repo_dir" rev-parse --verify "${requested_revision}^{commit}")
short_revision=${revision:0:12}
image_tag="knowyet-new-api:${short_revision}"
echo "Deploying commit $revision on $remote_host"
printf -v remote_command 'bash -s -- %q %q %q' "$deploy_dir" "$image_tag" "$revision"

# 阶段 2：只通过 SSH 发送部署命令，源码由服务器直接从 GitHub 获取。
ssh "$remote_host" "$remote_command" <<'REMOTE'
set -Eeuo pipefail
umask 077
export GIT_TERMINAL_PROMPT=0
deploy_dir=$1
image_tag=$2
revision=$3
SECONDS=0
# 阶段 3：检查服务器依赖、部署锁、现有配置及 Redis 健康状态。
cd "$deploy_dir"
for command in docker python3 flock git; do
  command -v "$command" >/dev/null || { echo "Missing remote command: $command" >&2; exit 1; }
done
docker buildx version >/dev/null || { echo 'Docker Buildx is required for BuildKit builds' >&2; exit 1; }
exec 9>"$deploy_dir/.deploy.lock"
flock -n 9 || { echo 'Another deployment is running' >&2; exit 1; }
[[ -f compose.yaml && -f .env && -f runtime.env && -f redis.conf ]] || {
  echo 'Existing compose.yaml, .env, runtime.env and redis.conf are required' >&2
  exit 1
}
docker compose up --help | grep -q -- '--wait-timeout' || {
  echo 'Docker Compose with --wait and --wait-timeout support is required' >&2
  exit 1
}
# An inherited shell variable must not override the image in the env file.
unset NEW_API_IMAGE
compose=(docker compose --env-file "$deploy_dir/.env" -f "$deploy_dir/compose.yaml")
redis_id=$("${compose[@]}" ps -q redis)
[[ -n $redis_id && $(docker inspect --format '{{.State.Health.Status}}' "$redis_id") == healthy ]] || {
  echo 'Redis must already be running and healthy; this script does not update Redis' >&2
  exit 1
}
# 阶段 4：在同一部署锁内更新固定源码目录，不覆盖服务器上的本地改动。
source_dir="$deploy_dir/new-api"
[[ -d "$source_dir/.git" ]] || {
  echo "Clone git@github.com:knowyetai/new-api.git into $source_dir before deploying" >&2
  exit 1
}
source_dir=$(cd "$source_dir" && pwd -P)
[[ $(git -C "$source_dir" rev-parse --show-toplevel) == "$source_dir" ]] || {
  echo 'Source directory must be the repository root' >&2
  exit 1
}
origin_url=$(git -C "$source_dir" remote get-url origin)
case "$origin_url" in
  git@github.com:knowyetai/new-api.git|https://github.com/knowyetai/new-api.git|https://github.com/knowyetai/new-api) ;;
  *) echo 'Source origin must be the knowyetai/new-api GitHub repository' >&2; exit 1 ;;
esac
if [[ -n $(git -C "$source_dir" status --porcelain --untracked-files=all --ignored=matching) ]]; then
  echo "Source checkout has local, untracked or ignored files: $source_dir. Resolve them before deploying; no files were removed." >&2
  exit 1
fi
git -C "$source_dir" fetch --no-tags origin "$revision"
[[ $(git -C "$source_dir" rev-parse --verify 'FETCH_HEAD^{commit}') == "$revision" ]] || {
  echo 'Fetched commit does not match requested revision' >&2
  exit 1
}
git -C "$source_dir" -c advice.detachedHead=false checkout --detach "$revision"
[[ $(git -C "$source_dir" rev-parse HEAD) == "$revision" ]] || { echo 'Checkout revision mismatch' >&2; exit 1; }
[[ -z $(git -C "$source_dir" status --porcelain --untracked-files=all --ignored=matching) ]] || {
  echo 'Source checkout is not clean after checkout' >&2
  exit 1
}
[[ -f "$source_dir/deploy/production/Dockerfile.cached" ]] || { echo 'Requested commit has no production Dockerfile' >&2; exit 1; }
echo "Source ready: $source_dir at $revision"

# 仅记录健康检查通过的提交，失败时保留上次成功版本，方便人工重新部署。
if [[ -f .deployment-successful-commit ]]; then
  echo "Last successful commit: $(cat .deployment-successful-commit)"
fi

# 阶段 5：回到部署根目录，以固定源码目录为上下文构建相同镜像。
# 提交号保存在 Git HEAD 和镜像标签，不写入源码目录。
cd "$deploy_dir"
echo "Building $image_tag"
build_started=$SECONDS
DOCKER_BUILDKIT=1 docker build --progress=plain --pull=false \
  -f "$source_dir/deploy/production/Dockerfile.cached" -t "$image_tag" \
  --label "org.opencontainers.image.revision=$revision" "$source_dir"

echo "Image build completed in $((SECONDS - build_started))s"

# 阶段 6：生成候选 .env，仅更新 NEW_API_IMAGE，并校验 Compose 配置。
# 校验通过才替换 .env；构建失败不会修改运行配置。
candidate=$(mktemp "$deploy_dir/.env.candidate.XXXXXX")
trap 'rm -f -- "$candidate"' EXIT
python3 - "$deploy_dir/.env" "$candidate" "$image_tag" <<'PY'
import pathlib
import re
import sys
source, target, image = sys.argv[1:]
lines = pathlib.Path(source).read_text().splitlines(keepends=True)
lines = [line for line in lines if not re.match(r'^\s*(?:export\s+)?NEW_API_IMAGE\s*=', line)]
text = ''.join(lines)
if text and not text.endswith('\n'):
    text += '\n'
pathlib.Path(target).write_text(text + 'NEW_API_IMAGE=' + image + '\n')
PY
docker compose --env-file "$candidate" -f "$deploy_dir/compose.yaml" config --quiet
chmod --reference=.env "$candidate"
mv -f -- "$candidate" .env
trap - EXIT

# 阶段 7：仅更新 new-api，等待健康检查；失败直接退出，由人指定 hash 重试。
echo "Updating new-api"
if ! "${compose[@]}" up -d --no-deps --wait --wait-timeout 180 new-api; then
  echo 'Deployment failed. Inspect new-api logs, then manually run deploy.sh with the desired commit hash.' >&2
  if [[ -f .deployment-successful-commit ]]; then
    echo "Last successful commit: $(cat .deployment-successful-commit)" >&2
  fi
  exit 1
fi
printf '%s\n' "$revision" > .deployment-successful-commit.tmp
mv -f .deployment-successful-commit.tmp .deployment-successful-commit
echo "Deployed $revision as $image_tag in ${SECONDS}s (remote total)"
REMOTE
