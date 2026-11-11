#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="$(mktemp -d)"
compose_started=false

cleanup() {
  if [[ "${compose_started}" == true ]]; then
    docker compose -f "${repository_root}/test/integration/compose.yml" down --volumes
  fi
  rm -rf "${work}"
}
trap cleanup EXIT

cd "${repository_root}"
docker compose -f test/integration/compose.yml up -d --wait
compose_started=true

go build -trimpath -o "${work}/fivemesh-backup" ./cmd/fivemesh-backup

backend_args=(
  --config "${work}/config.toml"
  --credentials "${work}/credentials.toml"
  --state-dir "${work}/state"
)

printf '%s\n' 'backup-test-password' |
  "${work}/fivemesh-backup" init \
    "${backend_args[@]}" \
    --profile integration \
    --host 127.0.0.1 \
    --port 33306 \
    --user backup_test \
    --database fxbackup_test \
    --backup-dir "${work}/backups" \
    --minimum-free-bytes 1048576 \
    --keep-last 2 \
    --keep-days 1 \
    --password-stdin

"${work}/fivemesh-backup" backup "${backend_args[@]}" --profile integration
archive="$(find "${work}/backups" -maxdepth 1 -name 'integration-*.sql.zst' -print -quit)"
"${work}/fivemesh-backup" verify "${backend_args[@]}" "${archive}"

cp "${archive}" "${work}/corrupt.sql.zst"
cp "${archive}.json" "${work}/corrupt.sql.zst.json"
cp "${archive}.sha256" "${work}/corrupt.sql.zst.sha256"
printf 'corruption' >>"${work}/corrupt.sql.zst"
if "${work}/fivemesh-backup" verify "${backend_args[@]}" "${work}/corrupt.sql.zst"; then
  echo "corrupted archive passed verification" >&2
  exit 1
fi

docker compose -f test/integration/compose.yml exec -T mariadb \
  mariadb --no-defaults -uroot -ptest-root-password \
  --execute="DROP DATABASE IF EXISTS fxbackup_restore; CREATE DATABASE fxbackup_restore;"

"${work}/fivemesh-backup" restore \
  "${backend_args[@]}" \
  --profile integration \
  --archive "${archive}" \
  --database fxbackup_restore \
  --yes

source_count="$(docker compose -f test/integration/compose.yml exec -T mariadb \
  mariadb --no-defaults -uroot -ptest-root-password --batch --skip-column-names \
  --execute='SELECT COUNT(*) FROM fxbackup_test.players')"
restored_count="$(docker compose -f test/integration/compose.yml exec -T mariadb \
  mariadb --no-defaults -uroot -ptest-root-password --batch --skip-column-names \
  --execute='SELECT COUNT(*) FROM fxbackup_restore.players')"

if [[ "${source_count}" != "${restored_count}" ]]; then
  echo "row-count mismatch: source=${source_count} restored=${restored_count}" >&2
  exit 1
fi

empty_work="${work}/empty"
mkdir -p "${empty_work}"
printf '%s\n' 'backup-test-password' |
  "${work}/fivemesh-backup" init \
    --config "${empty_work}/config.toml" \
    --credentials "${empty_work}/credentials.toml" \
    --state-dir "${empty_work}/state" \
    --profile empty \
    --host 127.0.0.1 \
    --port 33306 \
    --user backup_test \
    --database fxbackup_empty \
    --backup-dir "${empty_work}/backups" \
    --minimum-free-bytes 1048576 \
    --keep-last 1 \
    --password-stdin
"${work}/fivemesh-backup" backup \
  --config "${empty_work}/config.toml" \
  --credentials "${empty_work}/credentials.toml" \
  --state-dir "${empty_work}/state"
"${work}/fivemesh-backup" verify \
  --config "${empty_work}/config.toml" \
  --credentials "${empty_work}/credentials.toml" \
  --state-dir "${empty_work}/state" \
  --all

limited_work="${work}/limited"
mkdir -p "${limited_work}"
printf '%s\n' 'limited-test-password' |
  "${work}/fivemesh-backup" init \
    --config "${limited_work}/config.toml" \
    --credentials "${limited_work}/credentials.toml" \
    --state-dir "${limited_work}/state" \
    --profile limited \
    --host 127.0.0.1 \
    --port 33306 \
    --user backup_limited \
    --database fxbackup_test \
    --backup-dir "${limited_work}/backups" \
    --minimum-free-bytes 1048576 \
    --password-stdin
if "${work}/fivemesh-backup" backup \
  --config "${limited_work}/config.toml" \
  --credentials "${limited_work}/credentials.toml" \
  --state-dir "${limited_work}/state"; then
  echo "insufficiently privileged account unexpectedly completed a loadable dump" >&2
  exit 1
fi
if find "${limited_work}/backups" -maxdepth 1 -name '*.sql.zst' -print -quit | grep -q .; then
  echo "failed privilege test published an archive" >&2
  exit 1
fi

invalid_work="${work}/invalid"
mkdir -p "${invalid_work}"
printf '%s\n' 'wrong-password' |
  "${work}/fivemesh-backup" init \
    --config "${invalid_work}/config.toml" \
    --credentials "${invalid_work}/credentials.toml" \
    --state-dir "${invalid_work}/state" \
    --profile invalid \
    --host 127.0.0.1 \
    --port 33306 \
    --user backup_test \
    --database fxbackup_test \
    --backup-dir "${invalid_work}/backups" \
    --minimum-free-bytes 1048576 \
    --password-stdin
if "${work}/fivemesh-backup" backup \
  --config "${invalid_work}/config.toml" \
  --credentials "${invalid_work}/credentials.toml" \
  --state-dir "${invalid_work}/state"; then
  echo "invalid credentials unexpectedly completed a backup" >&2
  exit 1
fi
if grep -R 'wrong-password' "${invalid_work}/state"; then
  echo "password leaked into execution logs" >&2
  exit 1
fi

echo "Integration passed: source and restored row counts are ${source_count}; empty, corruption, privilege, and credential cases passed."

