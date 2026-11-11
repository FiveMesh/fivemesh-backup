#!/usr/bin/env bash
set -euo pipefail

prefix="${HOME}/.local"
binary_source=""
# GitHub Releases root. Downloads use <base>/latest/download/<file> or
# <base>/download/v<version>/<file>, matching GitHub's release URLs, so a
# mirror only needs the same layout.
base_url="${FIVEMESH_BACKUP_RELEASES:-https://github.com/FiveMesh/fivemesh-backup/releases}"
version="latest"
expected_sha256=""
run_setup=true
uninstall=false
assume_yes=false
remove_binary=false

usage() {
  cat <<'EOF'
FiveMesh Backup installer

Usage:
  install.sh [--version VERSION] [options]        download from GitHub Releases
  install.sh --binary PATH --sha256 HEX [options] install a local build

Options:
  --version VERSION  Release to install, e.g. 1.0.0 (default: latest)
  --sha256 HEX       Expected SHA-256; required with --binary. Downloads are
                     checked against the release's SHA256SUMS automatically.
  --binary PATH      Install a supplied Linux amd64/arm64 binary
  --base-url URL     Release mirror with the GitHub Releases layout
                     (default: https://github.com/FiveMesh/fivemesh-backup/releases)
  --prefix DIR       Installation prefix (default: ~/.local)
  --no-setup         Install only; do not launch the setup wizard
  --uninstall        Run the installed uninstall workflow
  --yes              Pass noninteractive confirmation to uninstall
  --remove-binary    With --uninstall, also remove the executable
  -h, --help         Show this help

Supports Linux amd64 and arm64. Runs without sudo and installs for the current
user. Re-run it to upgrade; configuration, schedules, and backups are kept.
EOF
}

die() {
  printf 'installer: %s\n' "$*" >&2
  exit 1
}

has_controlling_terminal() {
  (exec </dev/tty) 2>/dev/null
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)
      prefix="${2:?--prefix requires a value}"
      shift 2
      ;;
    --binary)
      binary_source="${2:?--binary requires a value}"
      shift 2
      ;;
    --base-url)
      base_url="${2:?--base-url requires a value}"
      shift 2
      ;;
    --version)
      version="${2:?--version requires a value}"
      shift 2
      ;;
    --sha256)
      expected_sha256="${2:?--sha256 requires a value}"
      shift 2
      ;;
    --no-setup)
      run_setup=false
      shift
      ;;
    --uninstall)
      uninstall=true
      shift
      ;;
    --yes)
      assume_yes=true
      shift
      ;;
    --remove-binary)
      remove_binary=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

[[ "$(uname -s)" == "Linux" ]] || die "unsupported operating system: $(uname -s); Linux is required"
case "$(uname -m)" in
  x86_64|amd64)
    architecture="amd64"
    ;;
  aarch64|arm64)
    architecture="arm64"
    ;;
  *)
    die "unsupported CPU architecture: $(uname -m)"
    ;;
esac
if [[ "$(id -u)" -eq 0 ]]; then
  die "run the installer as the account that will own backups, not as root"
fi

install_dir="${prefix}/bin"
destination="${install_dir}/fivemesh-backup"

if [[ "${uninstall}" == true ]]; then
  [[ -x "${destination}" ]] || die "installed executable not found at ${destination}"
  uninstall_args=(uninstall)
  if [[ "${assume_yes}" == true ]]; then
    uninstall_args+=(--yes)
  fi
  if [[ "${remove_binary}" == true ]]; then
    uninstall_args+=(--remove-binary)
  fi
  if [[ "${assume_yes}" != true ]] && has_controlling_terminal; then
    "${destination}" "${uninstall_args[@]}" </dev/tty
  else
    "${destination}" "${uninstall_args[@]}"
  fi
  exit 0
fi

if [[ -n "${binary_source}" && -z "${expected_sha256}" ]]; then
  die "--sha256 is required with --binary so the local binary can be verified"
fi

work="$(mktemp -d)"
cleanup() {
  rm -rf "${work}"
}
trap cleanup EXIT

if [[ -n "${binary_source}" ]]; then
  [[ -f "${binary_source}" ]] || die "binary not found: ${binary_source}"
  source_binary="${binary_source}"
else
  command -v curl >/dev/null 2>&1 || die "curl is required for downloading"
  artifact="fivemesh-backup-linux-${architecture}"
  if [[ "${version}" == "latest" ]]; then
    release_url="${base_url%/}/latest/download"
  else
    release_url="${base_url%/}/download/v${version#v}"
  fi
  source_binary="${work}/${artifact}"
  printf 'Downloading %s (%s)\n' "${artifact}" "${version}"
  curl -fsSL --proto '=https,file' --retry 3 --output "${source_binary}" "${release_url}/${artifact}" ||
    die "failed to download ${release_url}/${artifact}"
  if [[ -z "${expected_sha256}" ]]; then
    curl -fsSL --proto '=https,file' --retry 3 --output "${work}/SHA256SUMS" "${release_url}/SHA256SUMS" ||
      die "failed to download ${release_url}/SHA256SUMS"
    expected_sha256="$(awk -v name="${artifact}" '$2 == name || $2 == "*" name { print $1; exit }' "${work}/SHA256SUMS")"
    [[ -n "${expected_sha256}" ]] || die "SHA256SUMS does not list ${artifact}"
  fi
fi

actual_sha256=""
if command -v sha256sum >/dev/null 2>&1; then
  actual_sha256="$(sha256sum "${source_binary}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual_sha256="$(shasum -a 256 "${source_binary}" | awk '{print $1}')"
else
  die "sha256sum or shasum is required"
fi
if [[ "$(printf '%s' "${expected_sha256}" | tr '[:upper:]' '[:lower:]')" != "${actual_sha256}" ]]; then
  die "SHA-256 verification failed for ${source_binary}"
fi

mkdir -p "${install_dir}"
temporary_destination="${install_dir}/.fivemesh-backup.partial.$$"
cp "${source_binary}" "${temporary_destination}"
chmod 0755 "${temporary_destination}"
mv -f "${temporary_destination}" "${destination}"
"${destination}" version >/dev/null || die "installed binary failed its version check"

printf 'Installed FiveMesh Backup at %s\n' "${destination}"
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    printf 'Add %s to PATH, or run the executable using its full path.\n' "${install_dir}"
    ;;
esac

if [[ "${run_setup}" == true ]]; then
  if has_controlling_terminal; then
    "${destination}" setup </dev/tty
  elif [[ -t 0 ]]; then
    "${destination}" setup
  else
    die "no interactive terminal is available for setup; rerun with --no-setup or from an interactive shell"
  fi
fi

printf 'Installation complete.\n'

