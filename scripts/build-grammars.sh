#!/usr/bin/env bash
#
# Build tree-sitter grammar shared libraries for go-indexing-mcp.
# Downloads grammar C sources from GitHub and compiles them with zig cc.
# Outputs .so (Linux), .dylib (macOS), or .dll (cross-compiled) to
# ~/.go-mcp/tree-sitter/grammars/.
#
# Usage:
#   ./scripts/build-grammars.sh              # build for current OS
#   ./scripts/build-grammars.sh --release    # build + create release archive
#
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
GRAY='\033[0;37m'
NC='\033[0m'

GRAMMARS_DIR="${HOME}/.go-mcp/tree-sitter/grammars"
TMP_DIR="${TMPDIR:-/tmp}/tree-sitter-grammars"

# OS detection
case "$(uname -s)" in
  Linux)  EXT="so";     PREFIX="libtree-sitter-" ;;
  Darwin) EXT="dylib";  PREFIX="libtree-sitter-" ;;
  *)      echo "Unsupported OS: $(uname -s)"; exit 1 ;;
esac

# Grammar definitions: name:repo:ref:src_dir:has_scanner:extra_includes
GRAMMARS=(
  "go:tree-sitter/tree-sitter-go:v0.23.4:src:no"
  "python:tree-sitter/tree-sitter-python:v0.23.6:src:yes"
  "javascript:tree-sitter/tree-sitter-javascript:v0.23.1:src:yes"
  "typescript:tree-sitter/tree-sitter-typescript:v0.23.2:typescript/src:yes"
  "tsx:tree-sitter/tree-sitter-typescript:v0.23.2:tsx/src:yes"
  "c:tree-sitter/tree-sitter-c:v0.24.2:src:yes"
  "cpp:tree-sitter/tree-sitter-cpp:v0.23.4:src:yes"
  "html:tree-sitter/tree-sitter-html:v0.23.2:src:yes"
  "php:tree-sitter/tree-sitter-php:v0.24.2:php/src:yes:common"
  "css:tree-sitter/tree-sitter-css:v0.25.0:src:yes"
  "rust:tree-sitter/tree-sitter-rust:v0.24.2:src:yes"
  "json:tree-sitter/tree-sitter-json:v0.24.8:src:no"
  "zig:tree-sitter-grammars/tree-sitter-zig:v1.1.2:src:no"
)

mkdir -p "${GRAMMARS_DIR}"

echo -e "${CYAN}=== Tree-Sitter Grammar Builder ===${NC}"
echo -e "${GRAY}Output dir: ${GRAMMARS_DIR}${NC}"
echo -e "${GRAY}Extension:  .${EXT}${NC}"
echo ""

for entry in "${GRAMMARS[@]}"; do
  IFS=':' read -r -a parts <<< "${entry}"
  NAME="${parts[0]}"
  REPO="${parts[1]}"
  REF="${parts[2]}"
  SRC_DIR="${parts[3]}"
  HAS_SCANNER="${parts[4]}"
  EXTRA="${parts[5]:-}"

  OUT_FILE="${GRAMMARS_DIR}/${PREFIX}${NAME}.${EXT}"
  SRC_URL="https://github.com/${REPO}/archive/refs/tags/${REF}.zip"

  if [ -f "${OUT_FILE}" ]; then
    echo -e "${YELLOW}[${NAME}] already exists, skipping${NC}"
    continue
  fi

  echo -e "${GREEN}[${NAME}] downloading ${REPO}@${REF} ...${NC}"

  WORKDIR="${TMP_DIR}/${NAME}"
  mkdir -p "${WORKDIR}"

  ZIP_PATH="${WORKDIR}/src.zip"
  EXTRACTED_DIR="${WORKDIR}/extracted"

  # Download
  if ! curl -sfL "${SRC_URL}" -o "${ZIP_PATH}"; then
    echo -e "${RED}[${NAME}] download failed${NC}"
    rm -rf "${WORKDIR}"
    continue
  fi

  # Extract
  mkdir -p "${EXTRACTED_DIR}"
  if ! unzip -q "${ZIP_PATH}" -d "${EXTRACTED_DIR}"; then
    echo -e "${RED}[${NAME}] extract failed${NC}"
    rm -rf "${WORKDIR}"
    continue
  fi

  # Find extracted directory (repo-name format)
  PARENT_DIR="$(find "${EXTRACTED_DIR}" -mindepth 1 -maxdepth 1 -type d | head -1)"

  PARSER_C="${PARENT_DIR}/${SRC_DIR}/parser.c"
  SCANNER_C="${PARENT_DIR}/${SRC_DIR}/scanner.c"
  INCLUDE_DIR="${PARENT_DIR}/${SRC_DIR}"

  if [ ! -f "${PARSER_C}" ]; then
    echo -e "${RED}[${NAME}] parser.c not found at ${PARSER_C}${NC}"
    rm -rf "${WORKDIR}"
    continue
  fi

  # Build include flags
  INCLUDE_FLAGS=(-I"${INCLUDE_DIR}")
  if [ -n "${EXTRA}" ]; then
    INCLUDE_FLAGS+=(-I"${PARENT_DIR}/${EXTRA}")
  fi

  # Compile
  CC_ARGS=(
    cc
    -shared
    -O2
    -g0
    -fPIC
    "${INCLUDE_FLAGS[@]}"
    -o "${OUT_FILE}"
    "${PARSER_C}"
  )

  if [ "${HAS_SCANNER}" = "yes" ] && [ -f "${SCANNER_C}" ]; then
    CC_ARGS+=("${SCANNER_C}")
    echo -e "${GRAY}[${NAME}] including external scanner${NC}"
  fi

  echo -e "${GRAY}[${NAME}] compiling with zig cc ...${NC}"
  if ! zig "${CC_ARGS[@]}" 2>&1; then
    echo -e "${RED}[${NAME}] compilation failed${NC}"
    rm -rf "${WORKDIR}"
    continue
  fi

  if [ -f "${OUT_FILE}" ]; then
    SIZE="$(du -h "${OUT_FILE}" | cut -f1)"
    echo -e "${GREEN}[${NAME}] OK - ${SIZE}${NC}"
  else
    echo -e "${RED}[${NAME}] output not found${NC}"
  fi

  rm -rf "${WORKDIR}"
done

echo ""
echo -e "${CYAN}=== Done ===${NC}"
ls -lh "${GRAMMARS_DIR}/" 2>/dev/null || echo "(no files)"

# --release mode: create tar.gz for upload
if [ "${1:-}" = "--release" ]; then
  RELEASE_DIR="${TMP_DIR}/release"
  mkdir -p "${RELEASE_DIR}"
  tar -czf "${RELEASE_DIR}/grammars-linux-$(uname -m).tar.gz" -C "${GRAMMARS_DIR}" .
  echo -e "${GREEN}Release archive: ${RELEASE_DIR}/grammars-linux-$(uname -m).tar.gz${NC}"
  echo "Upload this file to GitHub Releases at:"
  echo "  https://github.com/cristian-guerrero/go-indexing-mcp/releases"
fi
