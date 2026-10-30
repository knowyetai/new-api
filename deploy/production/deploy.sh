#!/usr/bin/env bash
# Run locally after committing the release:
#   bash deploy/production/deploy.sh [user@host] [deployment-directory] [result-file]
# Defaults: root@8.145.51.132 /opt/knowyet-model-gateway
# 仅部署已提交的 HEAD；不询问或执行回滚。退出码 20 表示应用切换失败。
# 可选 result-file 为绝对路径，写入本次 release，供编排脚本调用 rollback.sh。
set -Eeuo pipefail

# 阶段 1：解析部署目标，检查本地工具和工作区状态。
if [[ ${1:-} == --help ]]; then
  sed -n '2,7p' "${BASH_SOURCE[0]}"
  exit 0
fi
if (( $# > 3 )); then
  echo 'Usage: bash deploy/production/deploy.sh [user@host] [deployment-directory] [result-file]' >&2
  exit 1
fi

remote_host=${1:-root@8.145.51.132}
deploy_dir=${2:-/opt/knowyet-model-gateway}
result_file=${3:-}
[[ -z $result_file || $result_file == /* ]] || { echo "result-file must be an absolute path" >&2; exit 1; }
# Keep SSH/SCP arguments and remote paths unambiguous.
[[ $remote_host =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.@-]*$ ]] || { echo 'Invalid SSH target' >&2; exit 1; }
[[ $deploy_dir =~ ^/[a-zA-Z0-9_/-]+$ && $deploy_dir != / ]] || { echo 'Invalid deployment directory' >&2; exit 1; }

for command in git ssh scp tar; do
  command -v "$command" >/dev/null || { echo "Missing local command: $command" >&2; exit 1; }
done
repo_dir=$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)
cd "$repo_dir"
if [[ -n $(git status --porcelain) ]]; then
  echo 'Commit or remove pending changes before deploying; only committed HEAD is packaged.' >&2
  exit 1
fi
# 阶段 2：确定提交号和镜像标签，仅打包已提交的源码（不包含 .git）。
revision=$(git rev-parse HEAD)
short_revision=$(git rev-parse --short=12 HEAD)
image_tag="knowyet-new-api:${short_revision}"
local_tmp=$(mktemp -d)
release="$(date -u +%Y%m%dT%H%M%SZ)-${short_revision}-${local_tmp##*.}"
# 本地正常结束或因错误退出时，删除临时目录及其中的源码压缩包。
trap 'rm -rf -- "$local_tmp"' EXIT
if [[ -n $result_file ]]; then
  printf '%s\n' "$release" > "$result_file"
fi
echo "Release: $release"
archive="$local_tmp/source.tar.gz"
git archive --format=tar.gz --output="$archive" "$revision"

# 阶段 3：创建远程独立上传目录，通过 SCP 上传源码压缩包。
# 每次使用唯一目录，避免并发上传相互覆盖；远程失败时保留上传文件供排查。
printf -v remote_command 'mkdir -p -- %q && mktemp -d %q' "$deploy_dir" "$deploy_dir/.upload-${release}-XXXXXX"
remote_upload=$(ssh "$remote_host" "$remote_command")
upload_prefix="$deploy_dir/.upload-${release}-"
upload_suffix=${remote_upload#"$upload_prefix"}
[[ $remote_upload == "$upload_prefix"* && $upload_suffix =~ ^[a-zA-Z0-9]{6}$ ]] || { echo 'Unexpected remote upload directory' >&2; exit 1; }
echo "Uploading commit $revision to $remote_host"
scp "$archive" "$remote_host:$remote_upload/source.tar.gz"
printf -v remote_command 'bash -s -- %q %q %q %q %q' "$deploy_dir" "$remote_upload" "$release" "$image_tag" "$revision"
# 阶段 4：通过 SSH 执行后续服务器操作；以下 REMOTE 内的命令均在服务器运行。
if ssh "$remote_host" "$remote_command" <<'REMOTE'
set -Eeuo pipefail
umask 077
deploy_dir=$1
upload_dir=$2
release=$3
image_tag=$4
revision=$5
# 阶段 5：检查服务器依赖、部署锁、现有配置及 Redis 健康状态。
cd "$deploy_dir"
for command in docker python3 flock tar; do
  command -v "$command" >/dev/null || { echo "Missing remote command: $command" >&2; exit 1; }
done
exec 9>"$deploy_dir/.deploy.lock"
flock -n 9 || { echo 'Another deployment is running' >&2; exit 1; }
# 在锁内检查未完成的部署或回滚，避免覆盖其切换标记和回滚基线。
# 空文件、目录或失效符号链接也不能视为已完成。
if [[ -e .deployment-switching || -L .deployment-switching ]]; then
  echo "An unfinished deployment or rollback exists: $deploy_dir/.deployment-switching. Resolve it before deploying again." >&2
  exit 1
fi
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
# 阶段 6：读取正在运行的版本，提前备份配置并保存回滚记录。
previous_container=$("${compose[@]}" ps -q new-api)
[[ -n $previous_container ]] || { echo 'A running new-api container is required to record a rollback version' >&2; exit 1; }
previous_image=$(docker inspect --format '{{.Config.Image}}' "$previous_container")
previous_image_id=$(docker inspect --format '{{.Image}}' "$previous_container")
# 提前保留旧镜像的独立标签，防止构建同一标签时丢失回滚引用。
docker tag "$previous_image_id" "knowyet-new-api:rollback-${release}"
previous_revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$previous_image_id")
backup="$deploy_dir/.env.before-${release}"
cp -p .env "$backup"
# 首次使用脚本时，以现有容器的镜像 ID 作为当前版本基线。
if [[ ! -f .deployment-current ]]; then
  printf '%s\n' "$previous_image_id" > .deployment-current
fi
previous_deployment=$(cat .deployment-current)
record="$deploy_dir/deployment-${release}.json"
python3 - "$record" "$release" "$previous_container" "$previous_image" "$previous_image_id" "$previous_revision" "$image_tag" "$revision" "$previous_deployment" <<'PY_RECORD'
import datetime
import json
import pathlib
import sys
path, release, container, image, image_id, revision, target, target_revision, previous_deployment = sys.argv[1:]
pathlib.Path(path).write_text(json.dumps({
    'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'release': release,
    'previous_deployment': previous_deployment,
    'previous_container': container,
    'previous_image': image,
    'previous_image_id': image_id,
    'previous_revision': revision,
    'target_image': target,
    'target_revision': target_revision,
}, indent=2) + '\n')
PY_RECORD
# 尝试记录可在构建前更新；当前版本只在容器切换成功后更新。
printf '%s\n' "$release" > .deployment-attempt.tmp
mv -f .deployment-attempt.tmp .deployment-attempt
echo "Current deployment: image=$previous_image image_id=$previous_image_id revision=$previous_revision"
echo "Rollback record: $record; configuration backup: $backup"

# 阶段 7：解压到本次发布的独立源码目录，并记录完整提交号。
# 此目录在部署成功或失败后均保留，便于追溯构建来源。
source_dir="$deploy_dir/build-source-${release}"
mkdir "$source_dir"
tar -xzf "$upload_dir/source.tar.gz" -C "$source_dir"
printf '%s\n' "$revision" > "$source_dir/SOURCE_COMMIT"
# 阶段 8：在源码根目录使用 Dockerfile.cached 构建带提交号标签的镜像。
# 构建失败时尚未修改运行配置；旧镜像和 Docker 构建缓存不会被脚本删除。
cd "$source_dir"
echo "Building $image_tag"
DOCKER_BUILDKIT=0 docker build --pull=false \
  -f deploy/production/Dockerfile.cached -t "$image_tag" \
  --label "org.opencontainers.image.revision=$revision" .
cd "$deploy_dir"

# 阶段 9：生成候选 .env，仅更新 NEW_API_IMAGE，并校验 Compose 配置。
# 校验或替换前出错时删除候选文件；原 .env 已在阶段 6 备份。
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
# 单独记录已进入切换阶段的发布，启动失败时据此定位可回滚的版本。
# 构建或配置校验失败不会修改此标记，也不会覆盖当前版本。
printf '%s\n' "$release" > .deployment-switching.tmp
mv -f .deployment-switching.tmp .deployment-switching
mv -f -- "$candidate" .env
trap - EXIT

# 阶段 10：仅更新 new-api，保持 Redis 不变，最多等待 180 秒通过健康检查。
# 启动可能触发数据库迁移，因此失败时保留现场，不自动回滚配置或容器。
echo "Updating new-api; previous configuration: $backup"
if ! "${compose[@]}" up -d --no-deps --wait --wait-timeout 180 new-api; then
  echo "Deployment failed. Inspect new-api logs. Previous config: $backup; source: $source_dir" >&2
  echo 'No automatic rollback was attempted; startup may have migrated the database.' >&2
  exit 20
fi
# 健康检查通过后才将发布编号记为当前版本，再清除切换标记。
printf '%s\n' "$release" > .deployment-current.tmp
mv -f .deployment-current.tmp .deployment-current
rm -f .deployment-switching
if ! "${compose[@]}" ps new-api; then
  echo 'Warning: deployment succeeded, but displaying container status failed.' >&2
fi
# 阶段 11：成功后删除远程上传目录及压缩包。
# 保留解压源码、.env 备份、应用数据、日志、旧镜像和构建缓存。
if ! rm -rf -- "$upload_dir"; then
  echo "Warning: deployment succeeded, but upload cleanup failed: $upload_dir" >&2
fi
echo "Deployed $revision as $image_tag"
REMOTE
then
  exit 0
else
  deploy_status=$?
fi

echo "Deployment failed (exit $deploy_status). No rollback was performed." >&2
exit "$deploy_status"
