#!/usr/bin/env bash
# Powerline-style status bar for Claude Code — full-width layout

input=$(cat)

# --- Colors (truecolor fg/bg) ---
reset='\033[0m'

bg_cwd='\033[48;2;68;68;102m'
fg_cwd='\033[38;2;220;220;255m'

bg_git='\033[48;2;40;90;60m'
fg_git='\033[38;2;180;255;180m'

bg_model='\033[48;2;80;50;110m'
fg_model='\033[38;2;230;200;255m'

bg_ctx='\033[48;2;100;70;30m'
fg_ctx='\033[38;2;255;220;140m'

bg_cost='\033[48;2;40;80;110m'
fg_cost='\033[38;2;160;220;255m'

bg_tokens='\033[48;2;60;75;90m'
fg_tokens='\033[38;2;180;210;240m'

bg_time='\033[48;2;50;50;50m'
fg_time='\033[38;2;200;200;200m'

# Fill color between left and right segments
bg_fill='\033[48;2;35;35;45m'

arrow=""

# --- Helper: shorten path ---
shorten_path() {
    local p="$1"
    local home="$HOME"
    p="${p/#$home/~}"
    if [ ${#p} -gt 40 ]; then
        p="...$(echo "$p" | rev | cut -d'/' -f1-2 | rev)"
    fi
    echo "$p"
}

# --- Terminal width (stty via /dev/tty is reliable in subprocess context) ---
term_width=$(stty size </dev/tty 2>/dev/null | awk '{print $2}')
[ -z "$term_width" ] || [ "$term_width" -le 0 ] 2>/dev/null && term_width=120

# --- Gather data ---

# CWD
cwd=$(echo "$input" | jq -r '.workspace.current_dir // .cwd // empty')
[ -z "$cwd" ] && cwd="$(pwd)"
short_cwd=$(shorten_path "$cwd")

# Git branch and status
git_info=""
if git -C "$cwd" rev-parse --is-inside-work-tree --no-optional-locks 2>/dev/null | grep -q true; then
    branch=$(git -C "$cwd" symbolic-ref --short HEAD 2>/dev/null || git -C "$cwd" rev-parse --short HEAD 2>/dev/null)
    if git -C "$cwd" status --porcelain 2>/dev/null | grep -q .; then
        git_info=" ${branch} *"
    else
        git_info=" ${branch}"
    fi
fi

# Model name
model=$(echo "$input" | jq -r '.model.display_name // empty')
[ -z "$model" ] && model="Claude"

# Context window usage — wider 20-block bar
used_pct=$(echo "$input" | jq -r '.context_window.used_percentage // empty')
if [ -n "$used_pct" ]; then
    used_int=$(printf '%.0f' "$used_pct")
    filled=$(( used_int * 20 / 100 ))
    bar=""
    for i in $(seq 1 20); do
        if [ "$i" -le "$filled" ]; then
            bar="${bar}█"
        else
            bar="${bar}░"
        fi
    done
    ctx_text="${bar} ${used_int}%"
else
    ctx_text="ctx: --"
fi

# Token counts
total_in=$(echo "$input" | jq -r '.context_window.total_input_tokens // 0')
total_out=$(echo "$input" | jq -r '.context_window.total_output_tokens // 0')
# Format with K/M suffix
fmt_tokens() {
    local n="$1"
    if [ "$n" -ge 1000000 ]; then
        awk "BEGIN { printf \"%.1fM\", $n / 1000000 }"
    elif [ "$n" -ge 1000 ]; then
        awk "BEGIN { printf \"%.1fK\", $n / 1000 }"
    else
        echo "$n"
    fi
}
tokens_text="in:$(fmt_tokens "$total_in") out:$(fmt_tokens "$total_out")"

# Session cost (use real cost from JSON)
real_cost=$(echo "$input" | jq -r '.cost.total_cost_usd // 0')
cost_text=$(awk "BEGIN { printf \"\$%.2f\", $real_cost }")

# Time
current_time=$(date +%H:%M:%S)

# --- Calculate visible lengths for padding ---
# Left segments: CWD + Git + Model
# Right segments: Context + Cost + Turns + Time

left_cwd=" ${short_cwd} "
left_git_content=""
if [ -n "$git_info" ]; then
    left_git_content=" ${git_info} "
else
    left_git_content="  no git "
fi
left_model=" ${model} "

right_ctx=" ${ctx_text} "
right_tokens=" ${tokens_text} "
right_cost=" ${cost_text} "
right_time=" ${current_time} "

# Count visible chars (each arrow = 1 char)
left_len=$(( ${#left_cwd} + 1 + ${#left_git_content} + 1 + ${#left_model} + 1 ))
right_len=$(( 1 + ${#right_ctx} + 1 + ${#right_tokens} + 1 + ${#right_cost} + 1 + ${#right_time} + 1 ))
fill_len=$(( term_width - left_len - right_len ))
[ "$fill_len" -lt 1 ] && fill_len=1

# Build fill string
fill=""
for (( i=0; i<fill_len; i++ )); do
    fill="${fill} "
done

# --- Build powerline bar ---

# Left segments
printf "${bg_cwd}${fg_cwd}${left_cwd}"
printf "${bg_git}\033[38;2;68;68;102m${arrow}${fg_git}${left_git_content}"
printf "${bg_model}\033[38;2;40;90;60m${arrow}${fg_model}${left_model}"

# Fill space
printf "${bg_fill}\033[38;2;80;50;110m${arrow}${fg_cwd}${fill}"

# Right segments
printf "${bg_ctx}\033[38;2;35;35;45m${arrow}${fg_ctx}${right_ctx}"
printf "${bg_tokens}\033[38;2;100;70;30m${arrow}${fg_tokens}${right_tokens}"
printf "${bg_cost}\033[38;2;60;75;90m${arrow}${fg_cost}${right_cost}"
printf "${bg_time}\033[38;2;40;80;110m${arrow}${fg_time}${right_time}"
printf "\033[0m\033[38;2;50;50;50m${arrow}${reset}\n"
