#!/bin/bash

# PiKVM-compatible MQTT Buffer Service Wrapper
# This script handles the persistent storage requirements for PiKVM

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="mqtt-buffer"
CONFIG_FILE="$SCRIPT_DIR/config.json"

# PiKVM persistent storage path will be set by kvmd-pstrun
BUFFER_FILE="${KVMD_PST_DATA:-/tmp}/mqtt-buffer.json"
LOGS_FILE="${KVMD_PST_DATA:-/tmp}/mqtt-buffer.log"

# Update config to use PST path
update_config_for_pst() {
    if [ -n "$KVMD_PST_DATA" ]; then
        echo "Using PiKVM persistent storage: $KVMD_PST_DATA"
        
        # Create a temporary config with updated paths using sed instead of jq
        TEMP_CONFIG=$(mktemp)
        if sed "s|\"persist_file\": \"[^\"]*\"|\"persist_file\": \"$BUFFER_FILE\"|g" \
            "$CONFIG_FILE" > "$TEMP_CONFIG" 2>/dev/null; then
            CONFIG_FILE="$TEMP_CONFIG"
            echo "Configuration updated for PST path"
        else
            echo "Warning: Could not update config file, using original"
            rm -f "$TEMP_CONFIG"
            TEMP_CONFIG=""
        fi
    else
        echo "Warning: Not running under kvmd-pstrun, using /tmp"
    fi
}

# Cleanup function
cleanup() {
    if [ -n "$TEMP_CONFIG" ] && [ -f "$TEMP_CONFIG" ]; then
        rm -f "$TEMP_CONFIG"
    fi
}
trap cleanup EXIT

# Main execution
main() {
    echo "Starting PiKVM MQTT Buffer Service..."
    echo "Timestamp: $(date)"
    echo "PST Data Path: ${KVMD_PST_DATA:-'Not set (using /tmp)'}"
    echo "Buffer file: $BUFFER_FILE"
    
    # Update configuration for PST
    update_config_for_pst
    
    # Check if buffer file exists and show stats
    if [ -f "$BUFFER_FILE" ]; then
        BUFFER_SIZE=$(stat -f%z "$BUFFER_FILE" 2>/dev/null || stat -c%s "$BUFFER_FILE" 2>/dev/null || echo "unknown")
        echo "Existing buffer file size: $BUFFER_SIZE bytes"
        
        # Simple check if buffer has content
        if [ -r "$BUFFER_FILE" ] && [ -s "$BUFFER_FILE" ]; then
            echo "Buffer file contains data"
        else
            echo "Buffer file is empty or unreadable"
        fi
    else
        echo "No existing buffer file found"
    fi
    
    # Export configuration path for the application
    export MQTT_BUFFER_CONFIG="$CONFIG_FILE"
    export MQTT_BUFFER_PST_PATH="$KVMD_PST_DATA"
    
    # Check if the mqtt-buffer binary exists
    if [ ! -f "$SCRIPT_DIR/mqtt-buffer" ]; then
        echo "Error: mqtt-buffer binary not found at $SCRIPT_DIR/mqtt-buffer"
        exit 1
    fi
    
    if [ ! -x "$SCRIPT_DIR/mqtt-buffer" ]; then
        echo "Error: mqtt-buffer binary is not executable"
        exit 1
    fi
    
    # Run the actual service
    echo "Starting MQTT buffer service..."
    echo "Binary: $SCRIPT_DIR/mqtt-buffer"
    echo "Config: $CONFIG_FILE"
    exec "$SCRIPT_DIR/mqtt-buffer"
}

# Handle signals gracefully
handle_signal() {
    echo "Received signal, shutting down gracefully..."
    if [ -n "$SERVICE_PID" ]; then
        kill "$SERVICE_PID" 2>/dev/null || true
        wait "$SERVICE_PID" 2>/dev/null || true
    fi
    exit 0
}

trap 'handle_signal' SIGTERM SIGINT

# Check if running directly or through kvmd-pstrun
if [ "$1" = "--direct" ]; then
    echo "Running in direct mode (not recommended for production)"
    main
elif [ -z "$KVMD_PST_DATA" ]; then
    echo "Error: This service should be run through kvmd-pstrun"
    echo "Usage: kvmd-pstrun -- $0"
    echo "Or for testing: $0 --direct"
    exit 1
else
    main
fi
