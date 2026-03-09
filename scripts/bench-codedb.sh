#!/usr/bin/env bash
set -euo pipefail

# === CodeDB Benchmark (ox port) ===
# Compares Rust, standalone Go, and ox codedb implementations.

RUST_BIN="${RUST_BIN:-/home/ajit/src/codedb/CodeDB/target/release/codedb-cli}"
GO_BIN="${GO_BIN:-/home/ajit/src/codedb/CodeDBGo/bin/codedb}"
OX_BIN="${OX_BIN:-$(go env GOPATH)/bin/ox}"
BENCH_DIR="/tmp/codedb-bench"
DATA_DIR="$BENCH_DIR/data"
LOG_DIR="$BENCH_DIR/logs"

declare -A REPO_URLS=(
  [sframerust]="https://github.com/ylow/SFrameRust"
  [tokio]="https://github.com/tokio-rs/tokio"
)

declare -A REPO_NAMES=(
  [sframerust]="SFrameRust"
  [tokio]="tokio"
)

declare -A TOOL_LABELS=(
  [rust]="Rust"
  [go]="Go"
  [ox]="ox"
)

TOOLS=(rust go ox)
REPOS=(sframerust)

SEARCH_QUERIES=(
  'spawn'
  'type:symbol Runtime'
  'lang:rust async'
  'type:diff streaming'
  'file:lib.rs fn'
)
SEARCH_ITERS=3

# === Associative arrays for results ===
declare -A R_INDEX_TIME R_PEAK_RSS R_REINDEX_TIME
declare -A R_SQLITE_MB R_FTS_MB R_GIT_MB R_TOTAL_MB
declare -A R_COMMITS R_BLOBS R_SYMBOLS R_SYMBOL_REFS R_FILE_REVS
declare -A R_SEARCH

# === Helpers ===

get_bin() {
  case "$1" in
    rust)   echo "$RUST_BIN" ;;
    go)     echo "$GO_BIN" ;;
    ox)     echo "$OX_BIN" ;;
  esac
}

get_sqlite_name() {
  case "$1" in
    rust) echo "db.sqlite" ;;
    go|ox) echo "metadata.db" ;;
  esac
}

get_fts_dir() {
  case "$1" in
    rust) echo "tantivy" ;;
    go|ox) echo "bleve" ;;
  esac
}

run_timed() {
  local timefile
  timefile=$(mktemp)
  set +e
  /usr/bin/time -v -o "$timefile" bash -c "$*" 2>&1
  local rc=$?
  set -e

  local wall_str
  wall_str=$(grep "Elapsed (wall clock)" "$timefile" | awk '{print $NF}')
  _wall_seconds=$(echo "$wall_str" | awk -F: '{
    n = NF
    if (n == 3) print $1*3600 + $2*60 + $3
    else if (n == 2) print $1*60 + $2
    else print $1
  }')

  local rss_kb
  rss_kb=$(grep "Maximum resident set size" "$timefile" | awk '{print $NF}')
  _peak_rss_mb=$(awk "BEGIN {printf \"%.0f\", $rss_kb / 1024}")

  rm -f "$timefile"
  return $rc
}

measure_sizes() {
  local tool="$1" root="$2"
  local sqlite_file="$root/$(get_sqlite_name "$tool")"
  local fts_dir="$root/$(get_fts_dir "$tool")"
  local repos_dir="$root/repos"

  _sqlite_mb=0; _fts_mb=0; _git_mb=0; _total_mb=0

  if [[ -f "$sqlite_file" ]]; then
    _sqlite_mb=$(du -sb "$sqlite_file" | awk "{printf \"%.2f\", \$1/1048576}")
  fi
  if [[ -d "$fts_dir" ]]; then
    _fts_mb=$(du -sb "$fts_dir" | awk "{printf \"%.2f\", \$1/1048576}")
  fi
  if [[ -d "$repos_dir" ]]; then
    _git_mb=$(du -sb "$repos_dir" | awk "{printf \"%.2f\", \$1/1048576}")
  fi
  _total_mb=$(du -sb "$root" | awk "{printf \"%.2f\", \$1/1048576}")
}

measure_db_stats() {
  local tool="$1" root="$2" bench_root="$3"
  local bin
  bin=$(get_bin "$tool")

  _sql_count() {
    if [[ "$tool" == "ox" ]]; then
      XDG_DATA_HOME="$bench_root/xdg" "$bin" codedb sql "$1" 2>/dev/null | tail -1 | grep -oP '\d+'
    else
      "$bin" --root "$root" sql "$1" 2>/dev/null | tail -1 | grep -oP '\d+'
    fi
  }

  _commits=$(_sql_count "SELECT COUNT(*) FROM commits")
  _blobs=$(_sql_count "SELECT COUNT(*) FROM blobs")
  _symbols=$(_sql_count "SELECT COUNT(*) FROM symbols")
  _symbol_refs=$(_sql_count "SELECT COUNT(*) FROM symbol_refs")
  _file_revs=$(_sql_count "SELECT COUNT(*) FROM file_revs")
}

run_index_cmd() {
  local tool="$1" root="$2" url="$3"
  local bin
  bin=$(get_bin "$tool")

  if [[ "$tool" == "ox" ]]; then
    # ox codedb uses XDG data dir; override via env
    XDG_DATA_HOME="$root/xdg" "$bin" codedb index "$url"
  else
    "$bin" --root "$root" index "$url"
  fi
}

run_search_cmd() {
  local tool="$1" root="$2" query="$3"
  local bin
  bin=$(get_bin "$tool")

  if [[ "$tool" == "ox" ]]; then
    XDG_DATA_HOME="$root/xdg" "$bin" codedb search "$query"
  else
    "$bin" --root "$root" search "$query"
  fi
}

run_searches() {
  local tool="$1" root="$2" repo="$3"
  local bin
  bin=$(get_bin "$tool")

  for qi in "${!SEARCH_QUERIES[@]}"; do
    local query="${SEARCH_QUERIES[$qi]}"
    local times=()
    for ((iter=0; iter<SEARCH_ITERS; iter++)); do
      local t_start t_end elapsed_ms
      t_start=$(date +%s%N)
      set +e
      if [[ "$tool" == "ox" ]]; then
        XDG_DATA_HOME="$root/xdg" "$bin" codedb search "$query" >/dev/null 2>&1
      else
        "$bin" --root "$root" search "$query" >/dev/null 2>&1
      fi
      set -e
      t_end=$(date +%s%N)
      elapsed_ms=$(( (t_end - t_start) / 1000000 ))
      times+=("$elapsed_ms")
    done
    IFS=$'\n' sorted=($(sort -n <<<"${times[*]}")); unset IFS
    local median=${sorted[$(( SEARCH_ITERS / 2 ))]}
    R_SEARCH["${tool}-${repo}-${qi}"]="$median"
  done
}

# === bench_one TOOL REPO_KEY ===
bench_one() {
  local tool="$1" repo="$2"
  local root="$DATA_DIR/${tool}-${repo}"
  local url="${REPO_URLS[$repo]}"
  local label="${TOOL_LABELS[$tool]}/${REPO_NAMES[$repo]}"

  echo ">>> Benchmarking $label"

  rm -rf "$root"
  mkdir -p "$root"

  # Full index
  echo "    Indexing (full)..."
  local logfile="$LOG_DIR/${tool}-${repo}-index.log"
  local bin
  bin=$(get_bin "$tool")
  local index_cmd
  if [[ "$tool" == "ox" ]]; then
    index_cmd="XDG_DATA_HOME='$root/xdg' '$bin' codedb index '$url'"
  else
    index_cmd="'$bin' --root '$root' index '$url'"
  fi
  set +e
  run_timed "$index_cmd" > "$logfile" 2>&1
  set -e
  R_INDEX_TIME["${tool}-${repo}"]="$_wall_seconds"
  R_PEAK_RSS["${tool}-${repo}"]="$_peak_rss_mb"
  echo "    Index: ${_wall_seconds}s, RSS: ${_peak_rss_mb}MB"

  # For ox, the actual data is in xdg/sageox/codedb
  local data_root="$root"
  if [[ "$tool" == "ox" ]]; then
    data_root="$root/xdg/sageox/codedb"
  fi

  # Sizes
  measure_sizes "$tool" "$data_root"
  R_SQLITE_MB["${tool}-${repo}"]="$_sqlite_mb"
  R_FTS_MB["${tool}-${repo}"]="$_fts_mb"
  R_GIT_MB["${tool}-${repo}"]="$_git_mb"
  R_TOTAL_MB["${tool}-${repo}"]="$_total_mb"

  # DB stats
  echo "    Collecting DB stats..."
  measure_db_stats "$tool" "$data_root" "$root"
  R_COMMITS["${tool}-${repo}"]="$_commits"
  R_BLOBS["${tool}-${repo}"]="$_blobs"
  R_SYMBOLS["${tool}-${repo}"]="$_symbols"
  R_SYMBOL_REFS["${tool}-${repo}"]="$_symbol_refs"
  R_FILE_REVS["${tool}-${repo}"]="$_file_revs"

  # Re-index
  echo "    Re-indexing (incremental)..."
  local reindex_log="$LOG_DIR/${tool}-${repo}-reindex.log"
  set +e
  run_timed "$index_cmd" > "$reindex_log" 2>&1
  set -e
  R_REINDEX_TIME["${tool}-${repo}"]="$_wall_seconds"
  echo "    Re-index: ${_wall_seconds}s"

  # Search latency
  echo "    Running search queries..."
  run_searches "$tool" "$root" "$repo"

  echo "    Done with $label"
  echo
}

# === Summary ===
print_summary() {
  local out="$BENCH_DIR/summary.txt"
  local cols=()
  local col_labels=()
  for tool in "${TOOLS[@]}"; do
    cols+=("$tool")
    col_labels+=("${TOOL_LABELS[$tool]}")
  done
  local ncols=${#cols[@]}

  {
    echo "=== CodeDB Benchmark Results ==="
    echo "Date: $(date +%Y-%m-%d)"
    echo

    for repo in "${REPOS[@]}"; do
      local name="${REPO_NAMES[$repo]}"
      echo "--- $name ---"

      # Header
      printf "%-20s" ""
      for lbl in "${col_labels[@]}"; do printf "  %-12s" "$lbl"; done
      echo

      # Metrics
      local metrics=(
        "Index time (s)"
        "Peak RSS (MB)"
        "Re-index time (s)"
        "SQLite DB (MB)"
        "FTS index (MB)"
        "Git repos (MB)"
        "Total data (MB)"
        "Commits"
        "Blobs"
        "Symbols"
        "Symbol refs"
        "File revs"
      )
      local arrays=(
        R_INDEX_TIME R_PEAK_RSS R_REINDEX_TIME
        R_SQLITE_MB R_FTS_MB R_GIT_MB R_TOTAL_MB
        R_COMMITS R_BLOBS R_SYMBOLS R_SYMBOL_REFS R_FILE_REVS
      )

      for mi in "${!metrics[@]}"; do
        printf "%-20s" "${metrics[$mi]}"
        local arr="${arrays[$mi]}"
        for tool in "${cols[@]}"; do
          local key="${tool}-${repo}"
          local ref="${arr}[$key]"
          printf "  %-12s" "${!ref:-n/a}"
        done
        echo
      done

      echo
      echo "Search (median ms):"
      for qi in "${!SEARCH_QUERIES[@]}"; do
        local query="${SEARCH_QUERIES[$qi]}"
        printf "  %-22s" "\"$query\""
        for tool in "${cols[@]}"; do
          local ref="R_SEARCH[${tool}-${repo}-${qi}]"
          printf "  %-12s" "${!ref:-n/a}"
        done
        echo
      done
      echo
    done
  } | tee "$out"

  echo "Summary saved to $out"
}

# === Main ===
mkdir -p "$DATA_DIR" "$LOG_DIR"

echo "=== CodeDB Benchmark ==="
echo "Tools: ${TOOLS[*]}"
echo "Data dir: $DATA_DIR"
echo

if [[ $# -gt 0 ]]; then
  REPOS=("$@")
fi

for repo in "${REPOS[@]}"; do
  for tool in "${TOOLS[@]}"; do
    bench_one "$tool" "$repo"
  done
done

print_summary
