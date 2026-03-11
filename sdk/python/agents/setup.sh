#!/bin/bash
# AetherNet agent setup — run once to create the venv and install dependencies.
# Usage: bash agents/setup.sh
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SDK_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VENV_DIR="$HOME/.aethernet-venv"

echo "AetherNet Agent Setup"
echo "====================="
echo ""

# ── 1. Create venv ────────────────────────────────────────────────────────────
if [ ! -d "$VENV_DIR" ]; then
    echo "Creating venv at $VENV_DIR ..."
    python3 -m venv "$VENV_DIR"
    echo "  done."
else
    echo "Venv already exists at $VENV_DIR — skipping creation."
fi
echo ""

# ── 2. Install SDK (editable) + dependencies ──────────────────────────────────
echo "Installing AetherNet SDK and dependencies ..."
"$VENV_DIR/bin/pip" install -q --upgrade pip
"$VENV_DIR/bin/pip" install -q -e "$SDK_DIR"
echo "  done."
echo ""

# ── 3. Check ANTHROPIC_API_KEY ────────────────────────────────────────────────
if [ -z "$ANTHROPIC_API_KEY" ]; then
    echo "ANTHROPIC_API_KEY is not set in your environment."
    echo "Get your key at https://console.anthropic.com/"
    echo ""
    echo "Set it now and re-run, or export it before running agents:"
    echo "  export ANTHROPIC_API_KEY=sk-ant-..."
    echo ""
else
    echo "ANTHROPIC_API_KEY detected."
    echo ""
fi

# ── 4. Ready ──────────────────────────────────────────────────────────────────
echo "Setup complete!  To start all agents:"
echo ""
echo "  export ANTHROPIC_API_KEY=sk-ant-...   # if not already set"
echo "  bash $SCRIPT_DIR/run_all.sh"
echo ""
