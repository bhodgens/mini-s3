#!/bin/bash
#
# show-bucket-actions.sh - Display all .bucket-actions configurations in a directory tree
#
# Usage: ./show-bucket-actions.sh [path]
#        path - Directory to search (default: current directory)
#
# This script finds all .bucket-actions files and displays a summary of their
# configured actions, including action names, patterns, and inactivity settings.

set -e

ROOT="${1:-.}"

# Colors for output (if terminal supports them)
if [[ -t 1 ]]; then
    BOLD='\033[1m'
    GREEN='\033[0;32m'
    YELLOW='\033[0;33m'
    CYAN='\033[0;36m'
    NC='\033[0m' # No Color
else
    BOLD=''
    GREEN=''
    YELLOW=''
    CYAN=''
    NC=''
fi

# Strip JSON5 comments for jq processing
strip_comments() {
    # Remove // comments (not in strings) and /* */ comments
    sed -e 's|//.*$||g' -e ':a;N;$!ba;s|/\*[^*]*\*\+\([^/*][^*]*\*\+\)*/||g'
}

# Check if jq is available
HAS_JQ=false
if command -v jq &>/dev/null; then
    HAS_JQ=true
fi

# Find all .bucket-actions files
echo -e "${BOLD}Scanning for .bucket-actions files in: $ROOT${NC}"
echo ""

found=0
while IFS= read -r -d '' file; do
    found=$((found + 1))
    echo -e "${BOLD}${CYAN}=== $file ===${NC}"

    if $HAS_JQ; then
        # Use jq for pretty parsing
        content=$(strip_comments < "$file")

        # Extract and display action summaries
        echo -e "${GREEN}after_upload:${NC}"
        echo "$content" | jq -r '.after_upload // [] | .[] | "  - \(.name)\(.enabled == false | if . then " (disabled)" else "" end)\(if .patterns then " [" + (.patterns | join(", ")) + "]" else "" end)"' 2>/dev/null || echo "  (none or parse error)"

        echo -e "${GREEN}after_download:${NC}"
        echo "$content" | jq -r '.after_download // [] | .[] | "  - \(.name)\(.enabled == false | if . then " (disabled)" else "" end)\(if .patterns then " [" + (.patterns | join(", ")) + "]" else "" end)"' 2>/dev/null || echo "  (none or parse error)"

        echo -e "${GREEN}after_delete:${NC}"
        echo "$content" | jq -r '.after_delete // [] | .[] | "  - \(.name)\(.enabled == false | if . then " (disabled)" else "" end)\(if .patterns then " [" + (.patterns | join(", ")) + "]" else "" end)"' 2>/dev/null || echo "  (none or parse error)"

        echo -e "${GREEN}inactivity_timeout:${NC}"
        inactivity=$(echo "$content" | jq -r '.inactivity_timeout | if . then "\(.duration)\(if .enabled == false then " (disabled)" else "" end) - \(.description // "no description")" else "none" end' 2>/dev/null)
        echo "  $inactivity"

        echo -e "${GREEN}inheritance:${NC}"
        inheritance=$(echo "$content" | jq -r '.inheritance.mode // "merge (default)"' 2>/dev/null)
        echo "  $inheritance"
    else
        # Fallback: just show the file contents
        echo -e "${YELLOW}(jq not available - showing raw content)${NC}"
        cat "$file"
    fi

    echo ""
done < <(find "$ROOT" -name ".bucket-actions" -print0 2>/dev/null)

if [[ $found -eq 0 ]]; then
    echo "No .bucket-actions files found in $ROOT"
    exit 0
fi

echo -e "${BOLD}Found $found .bucket-actions file(s)${NC}"
