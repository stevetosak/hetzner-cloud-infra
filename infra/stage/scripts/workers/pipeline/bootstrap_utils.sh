#!/bin/bash
# Utility functions for node bootstrap scripts
# -------------------------------------------

pause() {
    if [[ "${INTERACTIVE:-false}" == "true" ]]; then
        echo
        read -p "Press ENTER to continue..."
        echo
    fi
}

check() {
    echo "✔ $1"
}

run() {
    if [[ "${DRY_RUN:-false}" == "true" ]]; then
        echo "[DRY-RUN] $*"
    else
        eval "$@"
    fi
}
