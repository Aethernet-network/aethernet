#!/bin/bash
# Run all AetherNet worker agents and task poster.
# Usage: ANTHROPIC_API_KEY=sk-... ./run_all.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "AetherNet Agent Fleet"
echo "====================="
echo ""

# Post tasks first
echo "Posting tasks..."
python3 "$SCRIPT_DIR/task_poster.py"
echo ""

# Start worker agents in background
echo "Starting worker agents..."
python3 "$SCRIPT_DIR/research_agent.py" &
PID1=$!
echo "  research-worker-01 started (PID: $PID1)"

python3 "$SCRIPT_DIR/writing_agent.py" &
PID2=$!
echo "  writing-worker-01 started (PID: $PID2)"

python3 "$SCRIPT_DIR/code_agent.py" &
PID3=$!
echo "  code-worker-01 started (PID: $PID3)"

echo ""
echo "All agents running. Press Ctrl+C to stop."
echo ""

# Clean up children on exit
trap "kill $PID1 $PID2 $PID3 2>/dev/null; exit" INT TERM

# Wait for any to exit
wait
