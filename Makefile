# shabadoo — build, vendor, deploy.

BIN      := shabadoo
BIN_DIR  ?= $(HOME)/bin
CLAUDE_DIR ?= $(HOME)/.claude

# The personal payload overlay. Gitignored: `make vendor` fills it from this
# machine's live ~/.claude, and a fresh clone has only .gitkeep, so a public
# build ships the portable payload in config/ and nothing else.
LOCAL_DIR := config.local

# Stamped into the binary and reported by every node at login, so the dashboard
# can show which build each host is running. A hand-edited constant cannot do
# that job: the hazard it exists to catch is a *stale binary*, and a stale
# binary carries a stale constant that looks current.
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# The COMMIT date, not the build date: it stays identical for a given commit, so
# rebuilding the same source twice produces the same stamp and Docker layers
# still cache. `setup --service` compares this to decide whether it is about to
# replace a newer binary with an older one — git-describe output cannot be
# ordered, a timestamp can.
BUILT    := $(shell git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X main.version=$(VERSION) -X main.buildTime=$(BUILT)
GOBUILD   = go build -ldflags "$(LDFLAGS)"

.PHONY: build vet test install vendor vendor-check vendor-diff deploy dist clean version

build:
	$(GOBUILD) -o $(BIN) .

version:
	@echo $(VERSION)

vet:
	go vet ./...

# Depends on build so the repo binary is never stale when a test run passes.
# Verifying behaviour against a binary built before the change under test wasted
# real time three times in one evening, each time looking like a bug in the
# thing being tested.
test: build
	go test ./...

# Build straight into ~/bin (what the systemd unit runs), and refresh the repo
# binary alongside it. Both, because `setup --service` installs whatever binary
# is *running* — a stale ./shabadoo would silently downgrade the deployed one.
install: vet
	$(GOBUILD) -o $(BIN) .
	$(GOBUILD) -o $(BIN_DIR)/$(BIN) .
	@# The shorthand `setup` installs. This target writes the binary directly
	@# and never runs setup, so without this line the developer's own machine is
	@# the one host in the fleet that does not have it.
	@ln -sfn $(BIN_DIR)/$(BIN) $(BIN_DIR)/shaba && echo "  $(BIN_DIR)/shaba -> $(BIN)"

# The hub is a container on dm now, so there is no local hub unit to restart.
# `install` refreshes this host's agent and CLI; the hub is upgraded by building
# an image and shipping it (see README / homelab docs/shabadoo.md).
deploy: install
	sudo systemctl restart shabadoo-node
	systemctl --no-pager status shabadoo-node | grep -E 'Active|●' | head -2
	@echo
	@echo "note: this restarted THIS HOST'S AGENT only."
	@echo "      the hub runs on dm — upgrade it with:"
	@echo "        V=\$$(git describe --tags --always --dirty)"
	@echo "        B=\$$(git log -1 --format=%cI)"
	@echo "        docker build --load --build-arg VERSION=\$$V --build-arg BUILT=\$$B -t shabadoo:\$$V ."
	@echo "        docker save shabadoo:\$$V | gzip -1 | ssh user@coordinator 'gunzip | docker load'"
	@echo "        ssh user@coordinator \"cd /srv/shabadoo && sed -i 's/^SHABADOO_IMAGE_TAG=.*/SHABADOO_IMAGE_TAG=\$$V/' .env && docker compose up -d\""

# ---------------------------------------------------------------------------
# vendor: refresh the embedded payload from this machine's live config.
#
# The binary ships whatever is in scripts/ and config/ at BUILD time, so those
# trees are the source of truth for what `shabadoo setup` installs. This
# target pulls them back from the live ~/.claude + ~/bin so the repo can be
# updated after tweaking config in place. Review `make vendor-diff` first —
# vendoring is deliberate, never automatic, because it snapshots a host's
# personal config into the repo.
#
# Deliberately NOT vendored (per-machine / non-portable / private):
#   settings.local.json  mcp_settings.json  projects/  stats-cache.json
#   .credentials.json    history.jsonl      todos/     shell-snapshots/
#   CLAUDE.local.md      commands/
#
# CLAUDE.local.md is the machine overlay that CLAUDE.md imports: the project
# registry, the find whitelist, work toolchains. Keeping it out of VENDOR_FILES
# is what lets the shipped CLAUDE.md stay portable while the live one stays
# complete. commands/ is work-specific slash commands, same reasoning.
#
# rsync flags that matter:
#   -L         dereference symlinks. Several skills are symlinks into
#              ~/.agents/skills; copied as links they dangle in the repo and
#              go:embed skips symlinks *silently*, so those skills would be
#              missing from the binary with no error anywhere.
#   --exclude '*.bak.*'  setup leaves backups beside the files it replaces;
#              vendoring them would embed stale copies in the binary.
#   --exclude .git   skills/watch is its own git checkout; its .git has no
#              business in this repo or in the embedded payload.
# ---------------------------------------------------------------------------
VENDOR_EXCLUDES := --exclude='.git' --exclude='.gitmodules' --exclude='*.bak.*' \
                   --exclude='__pycache__' --exclude='*.pyc' --exclude='.venv' \
                   --exclude='node_modules' --exclude='.DS_Store'
VENDOR_FILES := CLAUDE.md settings.json claude-powerline.json \
                statusline-powerline.sh session-bridge-prompts.md
# plans/ is deliberately absent: those are per-session plan documents, scratch
# from whatever one machine was doing. Vendoring them shipped one WSL session's
# plan file to every node, which is how it was found.
VENDOR_DIRS  := skills agents hooks

# Tokens that must never reach the embedded payload. The payload ships to other
# machines and the binary is copied around, so client and product names have no
# business in it. `commands/` is excluded from VENDOR_DIRS above for the same
# reason: that whole tree is work-specific tooling, kept in ~/.claude only.
#
# This is a guard, not a filter — it fails the build rather than quietly
# stripping, because the fix belongs in the live config, not in a sed here.
# Read from a gitignored file, because the list is itself a list of client and
# product names — a denylist committed to a public repo publishes exactly what
# it exists to withhold. One token per line; absent means no tokens denied.
VENDOR_DENY := $(shell cat .vendor-deny 2>/dev/null)

# vendor fills config.local/ — the PERSONAL overlay, which git never sees.
#
# It used to write config/, which is why this repo could not be published: the
# committed payload was one operator's ~/.claude, hostnames and all. Scrubbing
# config/ would not have held, because this target is a straight copy and would
# have undone the scrub on the next run. Writing somewhere git ignores is what
# makes it stick.
# release TAG=v0.4.13 — push main, then exactly ONE tag.
#
# The one tag is the point. `git push --follow-tags` is the natural thing to
# type and it silently does nothing: GitHub suppresses the tag-push event when
# several arrive at once, so four releases went out with the release workflow
# never running and no image published for any of them. Nothing failed — a local
# tag looks exactly like a release until something tries to pull the image.
release:
	@# TAG, not VERSION: VERSION is already defined above for build stamping, so
	@# a `test -n` on it can never fail and the usage guard would never fire.
	@test -n "$(TAG)" || { echo "usage: make release TAG=v0.4.13"; exit 2; }
	@git diff --quiet || { echo "working tree is dirty; commit first"; exit 2; }
	@git rev-parse -q --verify "refs/tags/$(TAG)" >/dev/null || \
	  { echo "tag $(TAG) does not exist; create it with a message first"; exit 2; }
	@# The publish guard runs HERE, at the last moment anything is private.
	@# It existed and was only ever run by hand, so it caught a private project
	@# name in a commit message AFTER that commit was already on a public
	@# remote. A guard that has to be remembered is a guard that protects
	@# whoever remembers it.
	@RANGE="origin/main..HEAD" ./scripts_publish_check.sh || { echo; \
	  echo "refusing to push: the publish check failed above."; \
	  echo "A push is not reversible — the commit reaches a public remote and"; \
	  echo "stays reachable by SHA even after a rewrite."; exit 2; }
	@echo "pushing main..."
	@git push origin main
	@echo "pushing $(TAG) alone (one tag per push, deliberately)..."
	@git push origin "$(TAG)"
	@echo
	@echo "release workflow should now be running:"
	@echo "  gh run list --workflow=release.yml --limit 1"

vendor:
	@echo "vendoring config.local/ from $(CLAUDE_DIR)  (personal overlay, never committed)"
	@mkdir -p $(LOCAL_DIR)
	@for f in $(VENDOR_FILES); do \
	  if [ -f "$(CLAUDE_DIR)/$$f" ]; then cp -p "$(CLAUDE_DIR)/$$f" "$(LOCAL_DIR)/$$f"; echo "  $(LOCAL_DIR)/$$f"; \
	  else echo "  SKIP $$f (absent)"; fi; \
	done
	@# Prune what is no longer vendored. VENDOR_DIRS are rm -rf'd below, but the
	@# top level never was — so a file dropped from VENDOR_FILES, or a stray
	@# `settings.json.bak.*` copied in before the exclusion existed, kept
	@# shipping to every node forever. Found by a node reporting phantom
	@# backups it had never made.
	@keep=" $(VENDOR_FILES) $(VENDOR_DIRS) .gitkeep "; \
	for e in $(LOCAL_DIR)/* $(LOCAL_DIR)/.[!.]*; do \
	  [ -e "$$e" ] || continue; \
	  b=$$(basename "$$e"); \
	  case "$$keep" in *" $$b "*) continue;; esac; \
	  echo "  PRUNE $$e (no longer vendored)"; rm -rf "$$e"; \
	done
	@for d in $(VENDOR_DIRS); do \
	  if [ -d "$(CLAUDE_DIR)/$$d" ]; then \
	    rm -rf "$(LOCAL_DIR)/$$d"; mkdir -p "$(LOCAL_DIR)/$$d"; \
	    rsync -aL $(VENDOR_EXCLUDES) "$(CLAUDE_DIR)/$$d/" "$(LOCAL_DIR)/$$d/"; \
	    echo "  $(LOCAL_DIR)/$$d/"; \
	  else echo "  SKIP $$d/ (absent)"; fi; \
	done
	@touch $(LOCAL_DIR)/.gitkeep
	@$(MAKE) --no-print-directory vendor-check
	@echo "vendor complete — rebuild to embed: make build"

# Fail if a denied token reached the payload. Runs after every vendor, and
# stands alone so CI (or a paranoid moment) can check the tree as it is.
# The usual cause is work-specific content drifting back into ~/.claude/CLAUDE.md
# that belongs in the un-vendored ~/.claude/CLAUDE.local.md overlay instead.
# Two checks, and only the PUBLIC tree is scanned for tokens.
#
# config.local/ is deliberately exempt: it is the operator's own ~/.claude,
# untracked, embedded only into their own builds. Denying client names there
# would be denying them their own config. What matters is that nothing under it
# is tracked — which is the second check.
vendor-check:
	@fail=0; \
	for t in $(VENDOR_DENY); do \
	  hits=$$(grep -ril "$$t" config/ 2>/dev/null); \
	  if [ -n "$$hits" ]; then \
	    echo "DENIED token '$$t' in the embedded payload:"; \
	    echo "$$hits" | sed 's/^/    /'; \
	    fail=1; \
	  fi; \
	done; \
	if [ $$fail -eq 1 ]; then \
	  echo ""; \
	  echo "Move that content to ~/.claude/CLAUDE.local.md (imported, never vendored)."; \
	  exit 1; \
	fi; \
	echo "payload clean (no denied tokens)"
	@hits=$$(git ls-files $(LOCAL_DIR) | grep -v '^$(LOCAL_DIR)/.gitkeep$$' || true); \
	if [ -n "$$hits" ]; then \
	  echo "TRACKED files in the personal overlay — these would be published:"; \
	  echo "$$hits" | sed 's/^/    /'; \
	  echo ""; \
	  echo "git rm --cached them. Only .gitkeep belongs in git under $(LOCAL_DIR)/."; \
	  exit 1; \
	fi; \
	echo "overlay untracked (nothing personal would be published)"

# Show what vendoring would change, without changing it.
vendor-diff:
	@for f in $(VENDOR_FILES); do \
	  [ -f "$(CLAUDE_DIR)/$$f" ] || continue; \
	  diff -q "$(LOCAL_DIR)/$$f" "$(CLAUDE_DIR)/$$f" >/dev/null 2>&1 || echo "differs: $$f"; \
	done

# ---------------------------------------------------------------------------
# dist: cross-compiled binaries for bootstrapping other machines.
#
# This replaces the old `claude-install.sh` rsync-over-SSH bootstrap: instead
# of pulling files from this host, copy one self-contained binary and run
# `setup`. The payload is whatever was vendored here at build time, so a
# darwin binary built on WSL carries WSL's config — the same direction the
# rsync installer synced.
#
#   make dist
#   scp dist/shabadoo-darwin-arm64 mac:bin/shabadoo
#   ssh mac 'chmod +x bin/shabadoo && bin/shabadoo setup'
# ---------------------------------------------------------------------------
dist: vet
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64; do \
	  os=$${target%/*}; arch=$${target#*/}; \
	  GOOS=$$os GOARCH=$$arch $(GOBUILD) -o dist/$(BIN)-$$os-$$arch . || exit 1; \
	  echo "  dist/$(BIN)-$$os-$$arch"; \
	done

clean:
	rm -rf $(BIN) dist
