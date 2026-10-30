#!/usr/bin/env bash
# 在本地部署；应用切换失败时询问是否调用 rollback.sh：
#   bash deploy/production/deploy_with_rollback.sh [user@host] [deployment-directory]
# 默认目标：root@8.145.51.132 /opt/knowyet-model-gateway
set -Eeuo pipefail

# 阶段 1：解析目标，准备本次部署结果文件。
if [[ ${1:-} == --help ]]; then
  sed -n '2,4p' "${BASH_SOURCE[0]}"
  exit 0
fi
if (( $# > 2 )); then
  echo 'Usage: bash deploy_with_rollback.sh [user@host] [deployment-directory]' >&2
  exit 1
fi
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
remote_host=${1:-root@8.145.51.132}
deploy_dir=${2:-/opt/knowyet-model-gateway}
result_file=$(mktemp)
trap 'rm -f -- "$result_file"' EXIT

# 阶段 2：复用 deploy.sh，只有退出码 20 才表示需要询问回滚。
# 通过独立 Bash 运行部署脚本，并保留退出码供后续判断。
set +e
bash "$script_dir/deploy.sh" "$remote_host" "$deploy_dir" "$result_file"
deploy_status=$?
set -e
if (( deploy_status == 0 )); then
  exit 0
fi
if (( deploy_status != 20 )); then
  echo "Deployment stopped (exit $deploy_status). No rollback was requested; inspect remote state if SSH was interrupted." >&2
  exit "$deploy_status"
fi
release=$(cat "$result_file")
[[ -n $release ]] || { echo 'Missing deployment release; rollback cancelled' >&2; exit 20; }

# 阶段 3：从本地终端读取明确确认；无交互终端或拒绝时保留现场。
# SSH 的标准输入用于传脚本，因此确认不通过 SSH 标准输入传递。
if ! { exec 3<> /dev/tty; } 2>/dev/null; then
  echo "No interactive terminal; rollback was not performed. Record: $deploy_dir/deployment-${release}.json" >&2
  exit 20
fi
printf '部署失败。是否回滚到记录的旧镜像和配置？仅回滚应用，不恢复数据库；请确认旧版兼容当前数据库。[y/N] ' >&3
answer=''
read -r answer <&3 || exit 20
exec 3>&-
if [[ $answer != y && $answer != Y ]]; then
  echo 'Rollback declined; deployment files and containers were left for inspection.' >&2
  exit 20
fi

# 阶段 4：复用 rollback.sh；回滚成功仍返回 20，表示新版本部署未成功。
bash "$script_dir/rollback.sh" "$release" "$remote_host" "$deploy_dir"
exit 20
