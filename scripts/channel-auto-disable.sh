#!/usr/bin/env bash

set -u

INTERVAL_SECONDS=30
WINDOW_SECONDS=60
THRESHOLD_PERCENT=10
DISABLE_DURATION_SECONDS=60
API_URL="http://127.0.0.1:3000"
ADMIN_TOKEN=""
ADMIN_USER_ID=""
DOCKER_CONTAINER="postgres"
DB_USER="root"
DB_NAME="new-api"
WHITELIST_CHANNELS=()

usage() {
    cat <<'EOF'
Usage:
  channel-auto-disable.sh [options]

Required:
  --admin-token TOKEN       New API administrator access token
  --admin-user-id ID        New API administrator user ID

Time and threshold options:
  --interval SECONDS        Query interval, default: 30
  --window SECONDS          Log statistics window, default: 60
  --threshold PERCENT       Disable when error rate is strictly greater than this,
                            default: 10
  --disable-duration SEC    Disable duration, default: 60
Whitelist:
  --whitelist-channels IDS  Comma-separated channel IDs to skip, e.g. 21,22,29
  --whitelist-channel ID     Add one channel ID to the whitelist; repeatable


Connection options:
  --api-url URL             New API base URL, default: http://127.0.0.1:3000
  --docker-container NAME   PostgreSQL container, default: postgres
  --db-user USER            PostgreSQL user, default: root
  --db-name NAME            PostgreSQL database, default: new-api
  -h, --help                Show this help

Example:
  ./scripts/channel-auto-disable.sh \
    --interval 30 \
    --window 60 \
    --threshold 10 \
    --disable-duration 60 \
    --api-url http://127.0.0.1:3000 \
    --admin-token 'YOUR_TOKEN' \
    --admin-user-id 1
EOF
}

die() {
    printf '[%s] ERROR: %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*" >&2
    exit 1
}

log() {
    printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

require_positive_integer() {
    local name="$1"
    local value="$2"
    [[ "$value" =~ ^[1-9][0-9]*$ ]] || die "$name must be a positive integer: $value"
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --interval)
                [[ $# -ge 2 ]] || die "missing value for --interval"
                INTERVAL_SECONDS="$2"
                shift 2
                ;;
            --window)
                [[ $# -ge 2 ]] || die "missing value for --window"
                WINDOW_SECONDS="$2"
                shift 2
                ;;
            --threshold)
                [[ $# -ge 2 ]] || die "missing value for --threshold"
                THRESHOLD_PERCENT="$2"
                shift 2
                ;;
            --disable-duration)
                [[ $# -ge 2 ]] || die "missing value for --disable-duration"
                DISABLE_DURATION_SECONDS="$2"
                shift 2
                ;;
            --whitelist-channels)
                [[ $# -ge 2 ]] || die "missing value for --whitelist-channels"
                IFS="," read -ra channel_ids <<< "$2"
                for channel_id in "${channel_ids[@]}"; do
                    [[ "$channel_id" =~ ^[0-9]+$ ]] || die "invalid channel ID in --whitelist-channels: $channel_id"
                    WHITELIST_CHANNELS+=("$channel_id")
                done
                shift 2
                ;;
            --whitelist-channel)
                [[ $# -ge 2 ]] || die "missing value for --whitelist-channel"
                [[ "$2" =~ ^[0-9]+$ ]] || die "invalid channel ID in --whitelist-channel: $2"
                WHITELIST_CHANNELS+=("$2")
                shift 2
                ;;
            --api-url)
                [[ $# -ge 2 ]] || die "missing value for --api-url"
                API_URL="${2%/}"
                shift 2
                ;;
            --admin-token)
                [[ $# -ge 2 ]] || die "missing value for --admin-token"
                ADMIN_TOKEN="$2"
                shift 2
                ;;
            --admin-user-id)
                [[ $# -ge 2 ]] || die "missing value for --admin-user-id"
                ADMIN_USER_ID="$2"
                shift 2
                ;;
            --docker-container)
                [[ $# -ge 2 ]] || die "missing value for --docker-container"
                DOCKER_CONTAINER="$2"
                shift 2
                ;;
            --db-user)
                [[ $# -ge 2 ]] || die "missing value for --db-user"
                DB_USER="$2"
                shift 2
                ;;
            --db-name)
                [[ $# -ge 2 ]] || die "missing value for --db-name"
                DB_NAME="$2"
                shift 2
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

    require_positive_integer "--interval" "$INTERVAL_SECONDS"
    require_positive_integer "--window" "$WINDOW_SECONDS"
    require_positive_integer "--disable-duration" "$DISABLE_DURATION_SECONDS"
    [[ "$THRESHOLD_PERCENT" =~ ^[0-9]+$ ]] || die "--threshold must be an integer from 0 to 100"
    (( THRESHOLD_PERCENT <= 100 )) || die "--threshold must be an integer from 0 to 100"
    [[ "$ADMIN_TOKEN" != "" ]] || die "--admin-token is required"
    require_positive_integer "--admin-user-id" "$ADMIN_USER_ID"
}

check_dependencies() {
    command -v curl >/dev/null 2>&1 || die "curl is required"
    command -v docker >/dev/null 2>&1 || die "docker is required"
    command -v jq >/dev/null 2>&1 || die "jq is required"
}

query_recent_stats() {
    local sql
    sql=$(cat <<SQL
WITH recent_logs AS (
    SELECT *
    FROM logs
    WHERE type = 2
      AND created_at >= EXTRACT(EPOCH FROM NOW())::bigint - ${WINDOW_SECONDS}
      AND created_at < EXTRACT(EPOCH FROM NOW())::bigint
)
SELECT
    l.channel_id,
    COALESCE(c.name, '[渠道已删除]') AS channel_name,
    COUNT(DISTINCT l.user_id) AS unique_users,
    COUNT(*) AS total_requests,
    COUNT(*) FILTER (
        WHERE l.content LIKE '%上游没有返回计费信息，无法扣费（可能是上游超时）%'
    ) AS error_count,
    COUNT(DISTINCT l.user_id) FILTER (
        WHERE l.content LIKE '%上游没有返回计费信息，无法扣费（可能是上游超时）%'
    ) AS error_unique_users
FROM recent_logs l
LEFT JOIN channels c ON c.id = l.channel_id
GROUP BY l.channel_id, c.name
ORDER BY error_count DESC, total_requests DESC, l.channel_id;
SQL
)

    docker exec -i "$DOCKER_CONTAINER" \
        psql -U "$DB_USER" -d "$DB_NAME" -P pager=off \
        -v ON_ERROR_STOP=1 -At -F $'\t' -c "$sql"
}

update_channel_status() {
    local channel_id="$1"
    local status="$2"
    local response
    local payload

    payload=$(printf '{"id":%s,"status":%s}' "$channel_id" "$status")
    if ! response=$(curl -sS --fail-with-body -X PUT "${API_URL}/api/channel/" \
        -H "Authorization: Bearer ${ADMIN_TOKEN}" \
        -H "New-Api-User: ${ADMIN_USER_ID}" \
        -H 'Content-Type: application/json' \
        --data "$payload"); then
        return 1
    fi

    printf '%s' "$response" | jq -e '.success == true' >/dev/null 2>&1
}

declare -A DISABLED_UNTIL
declare -A WHITELIST_CHANNEL_SET

build_whitelist_set() {
    local channel_id
    for channel_id in "${WHITELIST_CHANNELS[@]}"; do
        WHITELIST_CHANNEL_SET["$channel_id"]=1
    done
}


disable_channel() {
    local channel_id="$1"
    local channel_name="$2"
    local total_requests="$3"
    local error_count="$4"
    local error_rate="$5"
    local now="$6"
    local until=$((now + DISABLE_DURATION_SECONDS))

    if ! update_channel_status "$channel_id" 2; then
        log "禁用失败: channel_id=${channel_id}, channel_name=${channel_name}"
        return
    fi

    DISABLED_UNTIL["$channel_id"]="$until"
    log "已禁用: channel_id=${channel_id}, channel_name=${channel_name}, total_requests=${total_requests}, error_count=${error_count}, error_rate=${error_rate}%, duration=${DISABLE_DURATION_SECONDS}s, enable_at=$(date -d "@${until}" '+%Y-%m-%d %H:%M:%S')"
}

enable_expired_channels() {
    local now="$1"
    local channel_id
    local until

    for channel_id in "${!DISABLED_UNTIL[@]}"; do
        until="${DISABLED_UNTIL[$channel_id]}"
        if (( now < until )); then
            continue
        fi

        if update_channel_status "$channel_id" 1; then
            log "已恢复启用: channel_id=${channel_id}, disabled_until=$(date -d "@${until}" '+%Y-%m-%d %H:%M:%S')"
            unset 'DISABLED_UNTIL[$channel_id]'
        else
            log "恢复启用失败，将在下一次检查重试: channel_id=${channel_id}"
        fi
    done
}

scan_channels() {
    local now="$1"
    local stats
    local channel_id
    local channel_name
    local unique_users
    local total_requests
    local error_count
    local error_unique_users
    local error_rate

    if ! stats=$(query_recent_stats); then
        log "查询 PostgreSQL 统计失败"
        return
    fi

    while IFS=$'\t' read -r channel_id channel_name unique_users total_requests error_count error_unique_users; do
        [[ "$channel_id" =~ ^[0-9]+$ ]] || continue
        if [[ -n "${WHITELIST_CHANNEL_SET[$channel_id]+x}" ]]; then
            continue
        fi
        [[ "$total_requests" =~ ^[0-9]+$ ]] || continue
        [[ "$error_count" =~ ^[0-9]+$ ]] || continue
        (( total_requests > 0 )) || continue

        # Compare as integers to avoid floating point rounding:
        # error_count / total_requests > threshold / 100.
        if (( error_count * 100 <= total_requests * THRESHOLD_PERCENT )); then
            continue
        fi
        if [[ -n "${DISABLED_UNTIL[$channel_id]+x}" ]] && (( now < DISABLED_UNTIL[$channel_id] )); then
            continue
        fi

        error_rate=$(awk -v errors="$error_count" -v total="$total_requests" \
            'BEGIN { printf "%.2f", errors * 100 / total }')
        disable_channel "$channel_id" "$channel_name" "$total_requests" "$error_count" "$error_rate" "$now"
    done <<< "$stats"
}

main() {
    parse_args "$@"
    check_dependencies
    build_whitelist_set

    local whitelist_display="none"
    if ((${#WHITELIST_CHANNELS[@]} > 0)); then
        whitelist_display=$(IFS=,; printf "%s" "${WHITELIST_CHANNELS[*]}")
    fi

    log "启动渠道自动禁用监控: interval=${INTERVAL_SECONDS}s, window=${WINDOW_SECONDS}s, threshold>${THRESHOLD_PERCENT}%, disable_duration=${DISABLE_DURATION_SECONDS}s, whitelist=${whitelist_display}, api=${API_URL}"

    local next_scan=0
    local now
    trap 'log "收到退出信号，停止监控"; exit 0' INT TERM

    while true; do
        now=$(date +%s)
        enable_expired_channels "$now"

        if (( now >= next_scan )); then
            scan_channels "$now"
            next_scan=$((now + INTERVAL_SECONDS))
        fi
        sleep 1
    done
}

main "$@"
