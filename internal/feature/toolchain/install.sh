#!/usr/bin/env bash
set -euo pipefail

# Flotilla toolchain Feature: common tooling ONLY. Installs no agent CLI (that is
# the profile's Install step) and no credentials (those are injected at runtime).
export DEBIAN_FRONTEND=noninteractive

if command -v apt-get >/dev/null 2>&1; then
	apt-get update -y
	apt-get install -y --no-install-recommends ca-certificates curl git gnupg
fi

# Node — needed for npm-based agent CLIs (claude, codex).
if ! command -v node >/dev/null 2>&1; then
	curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
	apt-get install -y --no-install-recommends nodejs
fi

# GitHub CLI — handy in-box for read-only ops (the engine still does all remote git).
if ! command -v gh >/dev/null 2>&1; then
	curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg |
		gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg
	echo "deb [signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
		>/etc/apt/sources.list.d/github-cli.list
	apt-get update -y && apt-get install -y --no-install-recommends gh
fi

# mise — polyglot tool manager (matches the engine's own toolchain story).
if ! command -v mise >/dev/null 2>&1; then
	curl -fsSL https://mise.run | sh
fi

echo "flotilla-toolchain installed"
