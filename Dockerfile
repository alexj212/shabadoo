# The coordinator as a container, for hosts that run Docker rather than a
# service manager. `setup --service` remains the way a *node* is installed —
# a node needs the host's tmux, so containerising one would be pointless.
#
# The binary is pure Go (modernc.org/sqlite, not cgo sqlite3), which is what
# makes CGO_ENABLED=0 and a static image possible at all — the same property
# `make dist` relies on to cross-compile a darwin binary from here.

# ---- build ----------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Module files first: this layer is cached unless the dependency set actually
# changes, so an ordinary source edit does not re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Stamped the same way the Makefile stamps it. An unstamped image reports
# "dev (unstamped)", which is deliberately not mistakable for a release — and
# the hub now reports this build to the dashboard, where it is compared
# against every node's.
ARG VERSION=dev
# BUILT is the commit date, and is what `setup --service` compares to refuse a
# downgrade — git-describe strings cannot be ordered, a timestamp can.
ARG BUILT=
RUN CGO_ENABLED=0 go build \
      -ldflags "-X main.version=${VERSION} -X main.buildTime=${BUILT}" \
      -o /out/shabadoo .

# ---- runtime --------------------------------------------------------------
FROM alpine:3.22

# ca-certificates is not needed under --trust-network, but is the moment the
# hub verifies Cloudflare Access JWTs against the team's cert endpoint. Adding
# it now costs ~200KB and avoids a confusing TLS failure later.
RUN apk add --no-cache ca-certificates

COPY --from=build /out/shabadoo /usr/local/bin/shabadoo

# Runs as UID 1000, the first non-root user on most systems — so bind-mounted state
# directory does not need to be world-writable for the hub to own its database.
USER 1000:1000

# State lives here: hub.db (enrolled device tokens, sessions, audit) and
# authorized_agents (the trust decision — which nodes may connect).
VOLUME ["/data"]
EXPOSE 8787

ENTRYPOINT ["/usr/local/bin/shabadoo"]
# No CMD: the arguments — bind address, database path, and above all the auth
# posture — are the deployment's decisions, so they belong in the compose file
# where they are visible, not baked into the image.
