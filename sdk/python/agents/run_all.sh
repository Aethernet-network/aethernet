#!/bin/bash
# Run all AetherNet worker agents and task poster.
# Usage: bash agents/run_all.sh
#
# First-time setup: bash agents/setup.sh
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SDK_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VENV_DIR="$HOME/.aethernet-venv"
VENV_PYTHON="$VENV_DIR/bin/python3"

echo "AetherNet Agent Fleet"
echo "====================="
echo ""

# ── 1. Activate venv if not already active ────────────────────────────────────
if [ -z "$VIRTUAL_ENV" ]; then
    if [ ! -f "$VENV_PYTHON" ]; then
        echo "ERROR: venv not found at $VENV_DIR"
        echo "Run setup first:  bash $SCRIPT_DIR/setup.sh"
        exit 1
    fi
    # shellcheck source=/dev/null
    source "$VENV_DIR/bin/activate"
    echo "Activated venv: $VENV_DIR"
else
    echo "Using active venv: $VIRTUAL_ENV"
fi

# ── 2. Verify dependencies ────────────────────────────────────────────────────
if ! "$VENV_PYTHON" -c "import aethernet, anthropic, cryptography" 2>/dev/null; then
    echo "ERROR: required packages not installed."
    echo "Run setup first:  bash $SCRIPT_DIR/setup.sh"
    exit 1
fi

# ── 3. Require ANTHROPIC_API_KEY ──────────────────────────────────────────────
if [ -z "$ANTHROPIC_API_KEY" ]; then
    echo "ERROR: ANTHROPIC_API_KEY is not set."
    echo "Get your key at https://console.anthropic.com/"
    echo "Then run:  export ANTHROPIC_API_KEY=sk-ant-..."
    exit 1
fi
echo "ANTHROPIC_API_KEY: set"
echo ""

# ── 4. Post seed tasks ────────────────────────────────────────────────────────
echo "Posting tasks to testnet ..."
"$VENV_PYTHON" "$SCRIPT_DIR/task_poster.py"
echo ""

# ── 5. Start worker agents in background ──────────────────────────────────────
echo "Starting worker agents ..."

"$VENV_PYTHON" "$SCRIPT_DIR/research_agent.py" &
PID1=$!
echo "  research-worker-01  (PID $PID1)"

"$VENV_PYTHON" "$SCRIPT_DIR/writing_agent.py" &
PID2=$!
echo "  writing-worker-01   (PID $PID2)"

"$VENV_PYTHON" "$SCRIPT_DIR/code_agent.py" &
PID3=$!
echo "  code-worker-01      (PID $PID3)"

echo ""
echo "All agents running.  Logs stream below.  Press Ctrl+C to stop."
echo ""

# ── 6. Clean up on exit ───────────────────────────────────────────────────────
trap "echo ''; echo 'Stopping agents...'; kill $PID1 $PID2 $PID3 2>/dev/null; exit" INT TERM

wait
