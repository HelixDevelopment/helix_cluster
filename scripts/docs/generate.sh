#!/bin/bash
# Generates all documentation exports per Constitution §11.4.15
# Usage: bash scripts/docs/generate.sh [format]
# Formats: md (default), html, pdf, docx, all

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DOCS_DIR="${PROJECT_ROOT}/docs"
EXPORT_DIR="${PROJECT_ROOT}/docs/export"
DOCPROCESSOR_DIR="${PROJECT_ROOT}/DocProcessor"
DOCPROCESSOR_BIN="${DOCPROCESSOR_DIR}/docprocessor"

FORMAT="${1:-md}"
TIMESTAMP="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Tracked docs per §11.4.44 — these are the canonical docs that require exports
TRACKED_DOCS=(
    "${DOCS_DIR}/continuation.md"
    "${DOCS_DIR}/issues.md"
    "${DOCS_DIR}/issues_summary.md"
    "${DOCS_DIR}/fixed.md"
    "${DOCS_DIR}/fixed_summary.md"
    "${DOCS_DIR}/HXC_REGISTRY.md"
)

mkdir -p "${EXPORT_DIR}"

# Build DocProcessor binary if needed
build_docprocessor() {
    if [ ! -x "${DOCPROCESSOR_BIN}" ]; then
        echo "[docs] Building DocProcessor..."
        cd "${DOCPROCESSOR_DIR}"
        # Resolve any merge conflicts in main.go before building
        if grep -q '^<<<<<<< ' ./cmd/docprocessor/main.go 2>/dev/null; then
            echo "[docs] WARNING: DocProcessor main.go has merge conflicts — using ours"
            git checkout --ours ./cmd/docprocessor/main.go 2>/dev/null || true
            git add ./cmd/docprocessor/main.go 2>/dev/null || true
        fi
        go build -o docprocessor ./cmd/docprocessor || {
            echo "[docs] WARNING: DocProcessor build failed — feature map extraction skipped"
            return 1
        }
        cd "${PROJECT_ROOT}"
    fi
}

# Update revision header in a markdown file per §11.4.44
# Format: **Revision:** N, **Last modified:** ISO8601 below H1
update_revision_header() {
    local file="$1"
    local basename_f=$(basename "${file}")
    
    # Extract current revision number (default 1)
    local rev=1
    if grep -q '^\*\*Revision:\*\*' "${file}" 2>/dev/null; then
        rev=$(grep '^\*\*Revision:\*\*' "${file}" | sed 's/.*:\*\* *//' | tr -d '[:space:]')
        rev=$((rev + 1))
    fi
    
    # Build new header block
    local new_header="**Revision:** ${rev}
**Last modified:** ${TIMESTAMP}"
    
    # Check if file already has §11.4.44 header block
    if grep -q '^\*\*Revision:\*\*' "${file}" 2>/dev/null; then
        # Replace existing Revision and Last modified lines
        sed -i.bak \
            -e "s|^\*\*Revision:\*\* .*|\*\*Revision:\*\* ${rev}|" \
            -e "s|^\*\*Last modified:\*\* .*|\*\*Last modified:\*\* ${TIMESTAMP}|" \
            "${file}" && rm -f "${file}.bak"
    else
        # No header yet — insert after H1 line
        awk -v rev="${rev}" -v ts="${TIMESTAMP}" '
            /^# / && !done {
                print
                print ""
                print "**Revision:** " rev
                print "**Last modified:** " ts
                done=1
                next
            }
            { print }
        ' "${file}" > "${file}.tmp" && mv "${file}.tmp" "${file}"
    fi
    
    # Remove legacy HTML comment revision headers if present
    if grep -q '^<!-- Revision:' "${file}" 2>/dev/null; then
        sed -i.bak '/^<!-- Revision:.*-->/d' "${file}" && rm -f "${file}.bak"
        # Also remove blank line that may be left at top
        if [ "$(head -1 "${file}" | wc -c)" -le 1 ]; then
            sed -i.bak '1d' "${file}" && rm -f "${file}.bak"
        fi
    fi
}

# Export single markdown to HTML via pandoc or DocProcessor
export_html() {
    local src="$1"
    local rel="${src#${PROJECT_ROOT}/}"
    local dst="${EXPORT_DIR}/${rel%.md}.html"
    mkdir -p "$(dirname "${dst}")"

    if command -v pandoc &>/dev/null; then
        pandoc -f markdown -t html5 -s \
            --metadata title="${rel%.md}" \
            -o "${dst}" "${src}"
    else
        echo "[docs] WARNING: pandoc not available, skipping HTML for ${rel}"
        return 1
    fi
}

# Export single markdown to PDF via pandoc+weasyprint or pandoc+pdflatex
export_pdf() {
    local src="$1"
    local rel="${src#${PROJECT_ROOT}/}"
    local dst="${EXPORT_DIR}/${rel%.md}.pdf"
    mkdir -p "$(dirname "${dst}")"

    if ! command -v pandoc &>/dev/null; then
        echo "[docs] WARNING: pandoc not available, skipping PDF for ${rel}"
        return 1
    fi

    # Prefer weasyprint (better CSS support, no LaTeX needed)
    if command -v weasyprint &>/dev/null; then
        local tmp_html="/tmp/docgen_$$_${rel//\//_}.html"
        pandoc -f markdown -t html5 -s \
            --metadata title="${rel%.md}" \
            -o "${tmp_html}" "${src}" 2>/dev/null || {
            echo "[docs] WARNING: HTML intermediate failed for ${rel}"
            rm -f "${tmp_html}"
            return 1
        }
        weasyprint "${tmp_html}" "${dst}" 2>/dev/null || {
            echo "[docs] WARNING: weasyprint failed for ${rel}"
            rm -f "${tmp_html}"
            return 1
        }
        rm -f "${tmp_html}"
        return 0
    fi

    # Fallback to pdflatex
    if ! command -v pdflatex &>/dev/null; then
        echo "[docs] WARNING: No PDF engine available (weasyprint or pdflatex), skipping PDF for ${rel}"
        return 1
    fi

    pandoc -f markdown -t pdf \
        --metadata title="${rel%.md}" \
        -o "${dst}" "${src}" 2>/dev/null || {
        echo "[docs] WARNING: PDF generation failed for ${rel}"
        return 1
    }
}

# Export single markdown to DOCX via pandoc
export_docx() {
    local src="$1"
    local rel="${src#${PROJECT_ROOT}/}"
    local dst="${EXPORT_DIR}/${rel%.md}.docx"
    mkdir -p "$(dirname "${dst}")"

    if ! command -v pandoc &>/dev/null; then
        echo "[docs] WARNING: pandoc not available, skipping DOCX for ${rel}"
        return 1
    fi

    pandoc -f markdown -t docx \
        --metadata title="${rel%.md}" \
        -o "${dst}" "${src}" || {
        echo "[docs] WARNING: DOCX generation failed for ${rel}"
        return 1
    }
}

# Run DocProcessor feature map extraction
run_docprocessor() {
    echo "[docs] Running DocProcessor feature map extraction..."
    build_docprocessor
    "${DOCPROCESSOR_BIN}" "${DOCS_DIR}" || {
        echo "[docs] WARNING: DocProcessor exited with code $?"
    }
}

# Main generation logic
echo "=== DocProcessor Integration — Generate Exports ==="
echo "Format: ${FORMAT}"
echo "Timestamp: ${TIMESTAMP}"
echo ""

# Determine which files to process based on format
if [ "${FORMAT}" = "tracked" ]; then
    # Only process the 6 canonical tracked docs (fast, for CI)
    ALL_MD_FILES=("${TRACKED_DOCS[@]}")
else
    # Find all .md files under docs/ and project root
    MD_FILES_DOCS=()
    while IFS= read -r -d '' f; do
        MD_FILES_DOCS+=("$f")
    done < <(find "${DOCS_DIR}" -type f -name "*.md" -print0 2>/dev/null || true)

    # Also include root-level markdown docs (README, CHANGELOG, etc.)
    MD_FILES_ROOT=()
    while IFS= read -r -d '' f; do
        MD_FILES_ROOT+=("$f")
    done < <(find "${PROJECT_ROOT}" -maxdepth 1 -type f -name "*.md" -print0 2>/dev/null || true)

    ALL_MD_FILES=("${MD_FILES_DOCS[@]}" "${MD_FILES_ROOT[@]}")
fi

if [ ${#ALL_MD_FILES[@]} -eq 0 ]; then
    echo "[docs] No markdown files found."
    exit 0
fi

# Update revision headers
echo "[docs] Updating revision headers..."
for f in "${ALL_MD_FILES[@]}"; do
    update_revision_header "${f}"
done

# Run DocProcessor for feature map / coverage (skip in tracked mode for speed)
if [ "${FORMAT}" != "tracked" ]; then
    run_docprocessor
fi

# Generate exports per format
FAILED=0

generate_for_format() {
    local fmt="$1"
    echo "[docs] Generating ${fmt} exports..."
    for f in "${ALL_MD_FILES[@]}"; do
        case "${fmt}" in
            html)
                export_html "${f}" || ((FAILED++)) || true
                ;;
            pdf)
                export_pdf "${f}" || ((FAILED++)) || true
                ;;
            docx)
                export_docx "${f}" || ((FAILED++)) || true
                ;;
        esac
    done
}

case "${FORMAT}" in
    md)
        echo "[docs] Markdown source only — no exports generated."
        ;;
    html)
        generate_for_format html
        ;;
    pdf)
        generate_for_format pdf
        ;;
    docx)
        generate_for_format docx
        ;;
    all)
        generate_for_format html
        generate_for_format pdf
        generate_for_format docx
        ;;
    tracked)
        # Fast path: only update revision headers for tracked docs, no exports
        echo "[docs] Tracked mode: updating revision headers only"
        ;;
    *)
        echo "Unknown format: ${FORMAT}"
        echo "Usage: bash scripts/docs/generate.sh [md|html|pdf|docx|all|tracked]"
        exit 1
        ;;
esac

# Verify exports succeeded
echo ""
echo "[docs] Verifying exports..."
if [ "${FORMAT}" = "all" ] || [ "${FORMAT}" = "html" ] || [ "${FORMAT}" = "pdf" ] || [ "${FORMAT}" = "docx" ]; then
    EXPECTED_COUNT=0
    ACTUAL_COUNT=0
    for f in "${ALL_MD_FILES[@]}"; do
        rel="${f#${PROJECT_ROOT}/}"
        if [ "${FORMAT}" = "all" ] || [ "${FORMAT}" = "html" ]; then
            EXPECTED_COUNT=$((EXPECTED_COUNT + 1))
            [ -f "${EXPORT_DIR}/${rel%.md}.html" ] && ACTUAL_COUNT=$((ACTUAL_COUNT + 1))
        fi
        if [ "${FORMAT}" = "all" ] || [ "${FORMAT}" = "pdf" ]; then
            EXPECTED_COUNT=$((EXPECTED_COUNT + 1))
            [ -f "${EXPORT_DIR}/${rel%.md}.pdf" ] && ACTUAL_COUNT=$((ACTUAL_COUNT + 1))
        fi
        if [ "${FORMAT}" = "all" ] || [ "${FORMAT}" = "docx" ]; then
            EXPECTED_COUNT=$((EXPECTED_COUNT + 1))
            [ -f "${EXPORT_DIR}/${rel%.md}.docx" ] && ACTUAL_COUNT=$((ACTUAL_COUNT + 1))
        fi
    done
    echo "[docs] Export verification: ${ACTUAL_COUNT}/${EXPECTED_COUNT} files present."
    if [ ${ACTUAL_COUNT} -ne ${EXPECTED_COUNT} ]; then
        echo "[docs] WARNING: Some exports are missing (pandoc/pdflatex may be unavailable)."
    fi
fi

if [ ${FAILED} -gt 0 ]; then
    echo "[docs] WARNING: ${FAILED} export operations failed."
fi

echo ""
echo "=== Documentation generation complete ==="
