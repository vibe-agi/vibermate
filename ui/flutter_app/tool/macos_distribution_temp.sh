distribution_temp_parent=
distribution_temp_directory=

create_distribution_temp() {
  local selected_parent="$1"
  local physical_parent owned_directory
  distribution_temp_parent=
  distribution_temp_directory=
  physical_parent="$(cd -P -- "${selected_parent}" && pwd -P)" || return 1
  owned_directory="$(mktemp -d "${physical_parent%/}/vibermate-flutter-distribution.XXXXXX")" || return 1
  distribution_temp_parent="${physical_parent}"
  distribution_temp_directory="${owned_directory}"
}

cleanup_distribution_temp() {
  local physical_parent="${distribution_temp_parent:-}"
  local owned_directory="${distribution_temp_directory:-}"
  local parent_prefix child current_parent
  parent_prefix="${physical_parent%/}/"
  child="${owned_directory#"${parent_prefix}"}"
  if [[ -z "${physical_parent}" || "${physical_parent}" != /* ||
    "${owned_directory}" == "${child}" || "${child}" == */* ||
    "${child}" != vibermate-flutter-distribution.?????? ||
    ! -d "${physical_parent}" || -L "${physical_parent}" ||
    ! -d "${owned_directory}" || -L "${owned_directory}" ]]; then
    echo "refusing to clean an unexpected distribution temporary path" >&2
    return 1
  fi
  current_parent="$(cd -P -- "${physical_parent}" && pwd -P)" || return 1
  if [[ "${current_parent}" != "${physical_parent}" ]]; then
    echo "refusing to clean an unexpected distribution temporary path" >&2
    return 1
  fi
  (
    cd -P -- "${physical_parent}" || return 1
    [[ ! -L "./${child}" && -d "./${child}" ]] || return 1
    rm -r -- "./${child}"
  )
}
