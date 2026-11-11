#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
cleanup() {
  rm -rf "${work}"
}
trap cleanup EXIT

cd "${repository_root}"
CGO_ENABLED=0 go build -trimpath -o "${work}/fivemesh-backup" ./cmd/fivemesh-backup
sha256="$(sha256sum "${work}/fivemesh-backup" | awk '{print $1}')"

bash install/install.sh \
  --binary "${work}/fivemesh-backup" \
  --sha256 "${sha256}" \
  --prefix "${work}/prefix" \
  --no-setup
"${work}/prefix/bin/fivemesh-backup" version >/dev/null

if bash install/install.sh \
  --binary "${work}/fivemesh-backup" \
  --sha256 deadbeef \
  --prefix "${work}/bad-prefix" \
  --no-setup; then
  echo "installer accepted an invalid checksum" >&2
  exit 1
fi

# Download path: a file:// mirror with the GitHub Releases layout.
case "$(uname -m)" in
  x86_64|amd64) artifact="fivemesh-backup-linux-amd64" ;;
  aarch64|arm64) artifact="fivemesh-backup-linux-arm64" ;;
esac
for release in "latest/download" "download/v9.9.9"; do
  mkdir -p "${work}/mirror/${release}"
  cp "${work}/fivemesh-backup" "${work}/mirror/${release}/${artifact}"
  printf '%s  %s\n' "${sha256}" "${artifact}" >"${work}/mirror/${release}/SHA256SUMS"
done
bash install/install.sh --base-url "file://${work}/mirror" --prefix "${work}/dl-latest" --no-setup >/dev/null
"${work}/dl-latest/bin/fivemesh-backup" version >/dev/null
bash install/install.sh --base-url "file://${work}/mirror" --version 9.9.9 --prefix "${work}/dl-pinned" --no-setup >/dev/null
"${work}/dl-pinned/bin/fivemesh-backup" version >/dev/null

printf 'deadbeef  %s\n' "${artifact}" >"${work}/mirror/latest/download/SHA256SUMS"
if bash install/install.sh --base-url "file://${work}/mirror" --prefix "${work}/dl-tampered" --no-setup 2>/dev/null; then
  echo "installer accepted a binary that does not match SHA256SUMS" >&2
  exit 1
fi
test ! -e "${work}/dl-tampered/bin/fivemesh-backup"

: >"${work}/mirror/latest/download/SHA256SUMS"
if bash install/install.sh --base-url "file://${work}/mirror" --prefix "${work}/dl-unlisted" --no-setup 2>/dev/null; then
  echo "installer accepted a release whose SHA256SUMS omits the artifact" >&2
  exit 1
fi

mkdir -p "${work}/user-data"
printf 'test-password\n' |
  "${work}/prefix/bin/fivemesh-backup" init \
    --config "${work}/user-data/config.toml" \
    --credentials "${work}/user-data/credentials.toml" \
    --state-dir "${work}/user-data/state" \
    --profile production \
    --host 127.0.0.1 \
    --port 3306 \
    --user backup \
    --database fxserver \
    --backup-dir "${work}/user-data/backups" \
    --password-stdin >/dev/null

bash install/install.sh \
  --uninstall \
  --prefix "${work}/prefix" \
  --yes \
  --remove-binary >/dev/null

test -f "${work}/user-data/config.toml"
test -f "${work}/user-data/credentials.toml"
test ! -e "${work}/prefix/bin/fivemesh-backup"
echo "Installer and uninstall checks passed."

