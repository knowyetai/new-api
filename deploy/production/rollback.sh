#!/usr/bin/env bash
# 在本地单独执行回滚（执行此命令即表示确认回滚）：
#   bash deploy/production/rollback.sh <release> [user@host] [deployment-directory]
# 默认目标：root@8.145.51.132 /opt/knowyet-model-gateway
# 根据部署记录恢复旧镜像和配置，不恢复数据库，不修改 Redis。
set -Eeuo pipefail

# 阶段 1：检查指定的发布编号和部署目标。
if [[ ${1:-} == --help ]]; then
  sed -n '2,5p' "${BASH_SOURCE[0]}"
  exit 0
fi
if (( $# < 1 || $# > 3 )); then
  echo 'Usage: bash rollback.sh <release> [user@host] [deployment-directory]' >&2
  exit 1
fi
release=$1
remote_host=${2:-root@8.145.51.132}
deploy_dir=${3:-/opt/knowyet-model-gateway}
[[ $release =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ ]] || { echo 'Invalid release' >&2; exit 1; }
[[ $remote_host =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.@-]*$ ]] || { echo 'Invalid SSH target' >&2; exit 1; }
[[ $deploy_dir =~ ^/[a-zA-Z0-9_/-]+$ && $deploy_dir != / ]] || { echo 'Invalid deployment directory' >&2; exit 1; }
command -v ssh >/dev/null || { echo 'Missing local command: ssh' >&2; exit 1; }

# 阶段 2：在服务器加锁并校验记录，避免覆盖后来启动的部署。
printf -v remote_command 'bash -s -- %q %q' "$deploy_dir" "$release"
ssh "$remote_host" "$remote_command" <<'ROLLBACK'
set -Eeuo pipefail
umask 077
deploy_dir=$1
release=$2
cd "$deploy_dir"
for command in docker python3 flock; do
  command -v "$command" >/dev/null || { echo "Missing remote command: $command" >&2; exit 1; }
done
exec 9>"$deploy_dir/.deploy.lock"
flock -n 9 || { echo 'Another deployment is running; rollback cancelled' >&2; exit 1; }
# 存在未完成的切换时，只允许回滚该次发布；否则回滚当前已成功切换的发布。
# 仅尝试构建的新发布不会阻止当前发布回滚。
rollback_target=$(cat .deployment-current)
if [[ -f .deployment-switching ]]; then
  rollback_target=$(cat .deployment-switching)
fi
[[ $rollback_target == "$release" ]] || { echo 'A different deployment is active or switching; rollback cancelled' >&2; exit 1; }
record="$deploy_dir/deployment-${release}.json"
previous_image_id=$(python3 - "$record" <<'PY_IMAGE'
import json
import pathlib
import sys
print(json.loads(pathlib.Path(sys.argv[1]).read_text())['previous_image_id'])
PY_IMAGE
)
previous_deployment=$(python3 - "$record" <<'PY_PREVIOUS'
import json
import pathlib
import sys
record = json.loads(pathlib.Path(sys.argv[1]).read_text())
print(record.get('previous_deployment') or record['previous_image_id'])
PY_PREVIOUS
)
# 阶段 3：使用独立标签固定旧镜像，避免原标签在重建时被覆盖；不拉取远程镜像。
rollback_image="knowyet-new-api:rollback-${release}"
docker image inspect "$previous_image_id" >/dev/null
docker tag "$previous_image_id" "$rollback_image"
# 阶段 4：以部署前的配置生成候选文件，校验后替换 .env。
candidate=$(mktemp "$deploy_dir/.env.rollback.XXXXXX")
trap 'rm -f -- "$candidate"' EXIT
python3 - "$deploy_dir/.env.before-${release}" "$candidate" "$rollback_image" <<'PY_ENV'
import pathlib
import re
import sys
source, target, image = sys.argv[1:]
lines = pathlib.Path(source).read_text().splitlines(keepends=True)
text = ''.join(line for line in lines if not re.match(r'^\s*(?:export\s+)?NEW_API_IMAGE\s*=', line))
if text and not text.endswith('\n'):
    text += '\n'
pathlib.Path(target).write_text(text + 'NEW_API_IMAGE=' + image + '\n')
PY_ENV
unset NEW_API_IMAGE
docker compose --env-file "$candidate" -f "$deploy_dir/compose.yaml" config --quiet
# 首次回滚保存失败配置；重试时保留原始现场，不以回滚配置覆盖它。
failed_backup="$deploy_dir/.env.failed-${release}"
if [[ ! -e $failed_backup && ! -L $failed_backup ]]; then
  cp -p .env "$failed_backup"
fi
chmod --reference="$deploy_dir/.env.before-${release}" "$candidate"
printf '%s\n' "$release" > .deployment-switching.tmp
mv -f .deployment-switching.tmp .deployment-switching
mv -f -- "$candidate" .env
trap - EXIT
# 阶段 5：只重建 new-api，等待健康检查，并记录回滚结果。
docker compose --env-file "$deploy_dir/.env" -f "$deploy_dir/compose.yaml" \
  up -d --no-deps --no-build --pull never --wait --wait-timeout 180 new-api
# 仅回滚健康检查通过后恢复当前版本记录，失败时保留切换标记供重试。
printf '%s\n' "$previous_deployment" > .deployment-current.tmp
mv -f .deployment-current.tmp .deployment-current
rm -f .deployment-switching
printf '%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ) $previous_image_id" > "$deploy_dir/rollback-${release}.txt"
echo "Rollback completed: $rollback_image ($previous_image_id). Failed deployment files were retained."
ROLLBACK
