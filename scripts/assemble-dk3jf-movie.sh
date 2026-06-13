#!/usr/bin/env bash
set -euo pipefail

frames_dir="${1:-}"
output_file="${2:-}"
fps="${3:-24}"

if [[ -z "$frames_dir" ]]; then
  echo "Usage: scripts/assemble-dk3jf-movie.sh <frames_dir> [output_file] [fps]"
  exit 1
fi

if [[ -z "$output_file" ]]; then
  output_file="$frames_dir/dk3jf-capture.mp4"
fi

if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ffmpeg not found. Please install ffmpeg first."
  exit 1
fi

ffmpeg \
  -y \
  -framerate "$fps" \
  -i "$frames_dir/frame_%05d.png" \
  -c:v libx264 \
  -pix_fmt yuv420p \
  -crf 20 \
  "$output_file"

echo "Movie written to: $output_file"
