#!/bin/sh
# Refuse to publish anything that identifies a private person, machine or client.
#
# A guard, not a filter: it fails rather than scrubbing, because a leak is not
# undone by a later commit — whatever is pushed is public from that moment.
#
# TWO pattern sets, and the split is the whole design:
#
#   structural   shapes that are private regardless of whose they are — RFC1918
#                addresses, real-looking emails, home directories. Safe to
#                publish, so they live here and run in CI.
#
#   local        the specific names of this operator's hosts, people and
#                clients. Read from .publish-deny, which git ignores.
#
# The first version of this script kept the specific tokens inline, and the
# published repository then contained a tidy list of exactly the private names
# it existed to withhold. That is the same mistake VENDOR_DENY made in the
# Makefile, reproduced in the tool written to catch it — a denylist is itself
# sensitive, and committing one publishes the thing.
set -u
fail=0

report() {
  [ -n "$2" ] || return 0
  echo "DENIED /$1/ :"
  echo "$2" | sed 's/^/    /' | head -12
  fail=1
}

# A line may opt out with `publish-check:allow`. Used only where naming the old
# thing IS the point — migration code that boots out a job installed under a
# previous label cannot avoid mentioning that label. Explicit and greppable, so
# the exemptions can be reviewed in one command. A scanner with no escape hatch
# gets disabled wholesale the first time it is wrong.
scan() {
  git ls-files -z | xargs -0 grep -InEi "$1" 2>/dev/null \
    | grep -v '^scripts_publish_check.sh:' \
    | grep -v 'publish-check:allow'
}

# Commit messages are as public as any tracked file, and were never checked.
#
# Not a theoretical gap: an account name reached a message here — quoted inside
# the very commit that explained why the scanner had flagged it — and survived
# until somebody went looking by hand. Removing it cost a history rewrite and a
# force-push across sixteen tags.
#
# Scanned separately because the FIX is different. A file is edited; a message
# is only fixable by rewriting history, which is cheap while a repository is
# private and expensive once it is not. Knowing before publishing is the value.
scan_messages() {
  git log --format='%h %s %b' --all 2>/dev/null \
    | grep -InEi "$1" \
    | grep -v 'publish-check:allow' \
    | cut -c1-160
}

# ---- structural: private by shape ------------------------------------------

# 192.168.x only. 172.16/12 is where Docker allocates its default bridges, so
# an address from it is a container's, not a person's home LAN — flagging it
# would train everyone to ignore this check. 10/8 is where the docs put their
# own examples. What is left is somebody's actual network.
report 'home-lan' "$(scan '\b192\.168\.[0-9]+\.[0-9]+\b' \
  | grep -vE '192\.168\.(0|1)\.(0|1|100)\b')"

# Real-looking email addresses. example.com/org/net and .test are reserved for
# documentation; anything else is a person.
report 'email' "$(scan '[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}' \
  | grep -viE '@(example\.(com|org|net)|.*\.example|example|test|localhost|noreply\.)' )"

# Home directories naming a real account rather than a placeholder.
report 'home-dir' "$(scan '/(home|Users)/[a-z0-9._-]+' \
  | grep -viE '/(home|Users)/(user|operator|me|a|you|USERNAME|<)' )"

# The same shapes in commit messages, which are published just as widely.
report 'home-dir (in a commit message)' "$(scan_messages '/(home|Users)/[a-z0-9._-]+' \
  | grep -viE '/(home|Users)/(user|operator|me|a|you|USERNAME|<)' )"
report 'private IP (in a commit message)' "$(scan_messages '192\.168\.[0-9]+\.[0-9]+')"

# ---- local: the specific names, from a file git never sees -----------------
if [ -f .publish-deny ]; then
  while IFS= read -r tok; do
    case "$tok" in ''|'#'*) continue ;; esac
    report "$tok" "$(scan "$tok")"
    report "$tok (in a commit message)" "$(scan_messages "$tok")"
  done < .publish-deny
else
  echo "note: .publish-deny absent — structural checks only."
  echo "      (expected in CI; locally it should exist, see CLAUDE.md)"
fi

if [ "$fail" -eq 1 ]; then
  echo
  echo "These are tracked files and would be published. Use placeholders:"
  echo "  coordinator.example  example.com  operator  /srv/shabadoo"
  exit 1
fi
echo "publish check: clean"
