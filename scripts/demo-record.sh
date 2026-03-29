#!/usr/bin/env bash
set -euo pipefail

# demo-record.sh — Record a terminal session for async demo playback.
#
# Uses asciinema if available, otherwise falls back to the 'script' command.
#
# Usage:
#   bash scripts/demo-record.sh              # start recording
#   bash scripts/demo-record.sh --replay FILE # replay a recording

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
RECORDINGS_DIR="$PROJECT_ROOT/demo-recordings"
mkdir -p "$RECORDINGS_DIR"

# Handle replay
if [ "${1:-}" = "--replay" ] && [ -n "${2:-}" ]; then
  FILE="$2"
  if [[ "$FILE" == *.cast ]]; then
    if command -v asciinema &>/dev/null; then
      exec asciinema play "$FILE"
    else
      echo "asciinema not installed. Install: brew install asciinema"
      exit 1
    fi
  else
    echo "Replaying script log (raw text):"
    cat "$FILE"
    exit 0
  fi
fi

echo "=== Settla Demo Recording ==="
echo ""

if command -v asciinema &>/dev/null; then
  OUTPUT="$RECORDINGS_DIR/demo-$TIMESTAMP.cast"
  echo "Recording with asciinema to: $OUTPUT"
  echo "Type 'exit' to stop recording."
  echo ""
  exec asciinema rec "$OUTPUT" --title "Settla Demo $TIMESTAMP"
else
  OUTPUT="$RECORDINGS_DIR/demo-$TIMESTAMP.log"
  echo "asciinema not found, using 'script' fallback."
  echo "Recording to: $OUTPUT"
  echo "Type 'exit' to stop recording."
  echo ""
  echo "TIP: Install asciinema for better recordings: brew install asciinema"
  echo ""
  exec script -q "$OUTPUT"
fi
