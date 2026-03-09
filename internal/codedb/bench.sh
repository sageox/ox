#!/usr/bin/env bash
set -euo pipefail

# === CodeDB Benchmark (Go / ox codedb) ===
# Benchmarks ox codedb index + search across multiple repos.
# Usage: ./bench.sh [repo_key ...]

OX_BIN="${OX_BIN:-$(cd "$(dirname "$0")/../.." && pwd)/bin/ox}"
BENCH_DIR="/tmp/codedb-bench"
DATA_DIR="$BENCH_DIR/data"
LOG_DIR="$BENCH_DIR/logs"

declare -A REPO_URLS=(
  [ox]="https://github.com/sageox/ox"
  [sframerust]="https://github.com/ylow/SFrameRust"
  [tokio]="https://github.com/tokio-rs/tokio"
)

declare -A REPO_NAMES=(
  [ox]="sageox/ox"
  [sframerust]="ylow/SFrameRust"
  [tokio]="tokio-rs/tokio"
)

REPOS=(ox sframerust tokio)

SEARCH_QUERIES=(
  'spawn'
  'type:symbol Runtime'
  'lang:go func'
  'type:diff streaming'
  'file:main.go func'
)
SEARCH_ITERS=3

# === Associative arrays for results ===
declare -A R_INDEX_TIME R_REINDEX_TIME
declare -A R_SQLITE_MB R_FTS_MB R_GIT_MB R_TOTAL_MB
declare -A R_COMMITS R_BLOBS R_FILE_REVS
declare -A R_SEARCH  # key: "repo-queryidx"

# === Helpers ===

# run_timed CMD [ARGS...]
# Sets: _wall_seconds
# macOS-compatible: uses bash SECONDS (integer precision)
run_timed() {
  local start end
  start=$(date +%s)
  set +e
  "$@" 2>&1
  local rc=$?
  set -e
  end=$(date +%s)
  _wall_seconds=$(( end - start ))
  return $rc
}

# measure_sizes DATA_ROOT
# Sets: _sqlite_mb, _fts_mb, _git_mb, _total_mb
measure_sizes() {
  local root="$1"
  _sqlite_mb=0; _fts_mb=0; _git_mb=0; _total_mb=0

  local sqlite_file="$root/metadata.db"
  local fts_dir="$root/bleve"
  local repos_dir="$root/repos"

  if [[ -f "$sqlite_file" ]]; then
    _sqlite_mb=$(du -sm "$sqlite_file" | awk '{print $1}')
  fi
  if [[ -d "$fts_dir" ]]; then
    _fts_mb=$(du -sm "$fts_dir" | awk '{print $1}')
  fi
  if [[ -d "$repos_dir" ]]; then
    _git_mb=$(du -sm "$repos_dir" | awk '{print $1}')
  fi
  _total_mb=$(du -sm "$root" | awk '{print $1}')
}

# measure_db_stats DATA_ROOT
# Sets: _commits, _blobs, _symbols, _symbol_refs, _file_revs
measure_db_stats() {
  local root="$1"
  _sql_count() {
    XDG_DATA_HOME="$root" "$OX_BIN" codedb sql "$1" 2>/dev/null | tail -1 | grep -oE '[0-9]+'
  }
  _commits=$(_sql_count "SELECT COUNT(*) FROM commits")
  _blobs=$(_sql_count "SELECT COUNT(*) FROM blobs")
  _file_revs=$(_sql_count "SELECT COUNT(*) FROM file_revs")
  # Symbol stats disabled — tree-sitter removed; see odvcencio/gotreesitter for upcoming replacement
  # _symbols=$(_sql_count "SELECT COUNT(*) FROM symbols")
  # _symbol_refs=$(_sql_count "SELECT COUNT(*) FROM symbol_refs")
}

# run_searches DATA_ROOT REPO_KEY
# Populates R_SEARCH["repo-idx"]
run_searches() {
  local root="$1" repo="$2"

  for qi in "${!SEARCH_QUERIES[@]}"; do
    local query="${SEARCH_QUERIES[$qi]}"
    local times=()
    for ((iter=0; iter<SEARCH_ITERS; iter++)); do
      local t_start t_end elapsed_ms
      t_start=$(python3 -c 'import time; print(int(time.time()*1000))')
      set +e
      XDG_DATA_HOME="$root" "$OX_BIN" codedb search "$query" >/dev/null 2>&1
      set -e
      t_end=$(python3 -c 'import time; print(int(time.time()*1000))')
      elapsed_ms=$(( t_end - t_start ))
      times+=("$elapsed_ms")
    done
    # Sort and take median
    IFS=$'\n' sorted=($(sort -n <<<"${times[*]}")); unset IFS
    local median=${sorted[$(( SEARCH_ITERS / 2 ))]}
    R_SEARCH["${repo}-${qi}"]="$median"
  done
}

# === bench_one REPO_KEY ===
bench_one() {
  local repo="$1"
  local root="$DATA_DIR/$repo"
  local codedb_root="$root/sageox/codedb"
  local url="${REPO_URLS[$repo]}"
  local label="${REPO_NAMES[$repo]}"

  echo ">>> Benchmarking $label"

  # Clean state
  rm -rf "$root"
  mkdir -p "$root"

  # Full index — XDG_DATA_HOME points ox codedb at our bench dir
  echo "    Indexing (full)..."
  local logfile="$LOG_DIR/${repo}-index.log"
  set +e
  run_timed env XDG_DATA_HOME="$root" "$OX_BIN" codedb index "$url" > "$logfile" 2>&1
  set -e
  R_INDEX_TIME["${repo}"]="$_wall_seconds"
  echo "    Index: ${_wall_seconds}s"

  # Sizes
  measure_sizes "$codedb_root"
  R_SQLITE_MB["${repo}"]="$_sqlite_mb"
  R_FTS_MB["${repo}"]="$_fts_mb"
  R_GIT_MB["${repo}"]="$_git_mb"
  R_TOTAL_MB["${repo}"]="$_total_mb"

  # DB stats
  echo "    Collecting DB stats..."
  measure_db_stats "$root"
  R_COMMITS["${repo}"]="$_commits"
  R_BLOBS["${repo}"]="$_blobs"
  R_FILE_REVS["${repo}"]="$_file_revs"

  # Re-index (incremental, no new commits)
  echo "    Re-indexing (incremental)..."
  local reindex_log="$LOG_DIR/${repo}-reindex.log"
  set +e
  run_timed env XDG_DATA_HOME="$root" "$OX_BIN" codedb index "$url" > "$reindex_log" 2>&1
  set -e
  R_REINDEX_TIME["${repo}"]="$_wall_seconds"
  echo "    Re-index: ${_wall_seconds}s"

  # Search latency
  echo "    Running search queries..."
  run_searches "$root" "$repo"

  echo "    Done with $label"
  echo
}

# === Summary Printer ===
print_summary() {
  local out="$BENCH_DIR/summary.txt"

  # Build column headers from REPOS
  local hdr_fmt="%-26s"
  local val_fmt="%-26s"
  for _ in "${REPOS[@]}"; do
    hdr_fmt+=" %-16s"
    val_fmt+=" %-16s"
  done
  hdr_fmt+="\n"
  val_fmt+="\n"

  {
    echo "=== CodeDB Benchmark Results (ox codedb) ==="
    echo "Date: $(date +%Y-%m-%d)"
    echo "Binary: $OX_BIN"
    echo

    # Header row
    local names=()
    for repo in "${REPOS[@]}"; do
      names+=("${REPO_NAMES[$repo]}")
    done
    printf "$hdr_fmt" "" "${names[@]}"

    # Metric rows
    local vals

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_INDEX_TIME[$r]}"); done
    printf "$val_fmt" "Index time (s):" "${vals[@]}"

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_REINDEX_TIME[$r]}"); done
    printf "$val_fmt" "Re-index time (s):" "${vals[@]}"

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_SQLITE_MB[$r]}"); done
    printf "$val_fmt" "SQLite DB (MB):" "${vals[@]}"

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_FTS_MB[$r]}"); done
    printf "$val_fmt" "FTS/Bleve (MB):" "${vals[@]}"

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_GIT_MB[$r]}"); done
    printf "$val_fmt" "Git repos (MB):" "${vals[@]}"

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_TOTAL_MB[$r]}"); done
    printf "$val_fmt" "Total data (MB):" "${vals[@]}"

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_COMMITS[$r]}"); done
    printf "$val_fmt" "Commits:" "${vals[@]}"

    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_BLOBS[$r]}"); done
    printf "$val_fmt" "Blobs:" "${vals[@]}"

    # Symbol stats disabled — tree-sitter removed; see odvcencio/gotreesitter
    vals=(); for r in "${REPOS[@]}"; do vals+=("${R_FILE_REVS[$r]}"); done
    printf "$val_fmt" "File revs:" "${vals[@]}"

    echo
    echo "Search latency (median ms):"
    for qi in "${!SEARCH_QUERIES[@]}"; do
      local query="${SEARCH_QUERIES[$qi]}"
      vals=()
      for r in "${REPOS[@]}"; do
        vals+=("${R_SEARCH[${r}-${qi}]:-n/a}")
      done
      printf "  %-24s" "\"$query\""
      for v in "${vals[@]}"; do
        printf " %-16s" "$v"
      done
      printf "\n"
    done
    echo
  } | tee "$out"

  echo "Summary saved to $out"
}

# === Main ===
mkdir -p "$DATA_DIR" "$LOG_DIR"

echo "=== CodeDB Benchmark ==="
echo "Binary: $OX_BIN"
echo "Data dir: $DATA_DIR"
echo

# Allow running a subset: bench.sh [repo...]
if [[ $# -gt 0 ]]; then
  REPOS=("$@")
fi

for repo in "${REPOS[@]}"; do
  bench_one "$repo"
done

print_summary
