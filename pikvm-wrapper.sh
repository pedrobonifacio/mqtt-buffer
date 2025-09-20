#!/bin/bash

# PiKVM-compatible MQTT Buffer Service Wrapper
# This script handles the persistent storage requirements for PiKVM

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="mqtt-buffer"
CONFIG_FILE="$SCRIPT_DIR/config.json"

# PiKVM persistent storage path will be set by kvmd-pstrun
BUFFER_FILE="${KVMD_PST_DATA:-/tmp}/mqtt-buffer.json"

# Track the service process and temp config
SERVICE_PID=""
TEMP_CONFIG=""

# Update config to use PST path
update_config_for_pst() {
    if [ -n "$KVMD_PST_DATA" ]; then
        echo "Using PiKVM persistent storage: $KVMD_PST_DATA"
        
        # Create a temporary config in PST area instead of root filesystem
        TEMP_CONFIG="$KVMD_PST_DATA/mqtt-buffer-config.tmp"
        if sed "s|\"persist_file\": \"[^\"]*\"|\"persist_file\": \"$BUFFER_FILE\"|g" \
            "$CONFIG_FILE" > "$TEMP_CONFIG" 2>/dev/null; then
            CONFIG_FILE="$TEMP_CONFIG"
            echo "Configuration updated for PST path"
        else
            echo "Warning: Could not update config file, using original"
            rm -f "$TEMP_CONFIG" 2>/dev/null || true
            TEMP_CONFIG=""
        fi
    else
        echo "Warning: Not running under kvmd-pstrun, using /tmp"
    fi
}

# Cleanup function
cleanup() {
    echo "Cleaning up..."
    
    # Stop the service if it's running
    if [ -n "$SERVICE_PID" ] && kill -0 "$SERVICE_PID" 2>/dev/null; then
        echo "Stopping service (PID: $SERVICE_PID)..."
        kill -TERM "$SERVICE_PID" 2>/dev/null || true
        
        # Wait for graceful shutdown
        local count=0
        while kill -0 "$SERVICE_PID" 2>/dev/null && [ $count -lt 10 ]; do
            sleep 1
            count=$((count + 1))
        done
        
        # Force kill if still running
        if kill -0 "$SERVICE_PID" 2>/dev/null; then
            echo "Force killing service..."
            kill -KILL "$SERVICE_PID" 2>/dev/null || true
        fi
        
        wait "$SERVICE_PID" 2>/dev/null || true
        echo "Service stopped"
    fi
    
    # Clean up temporary config (in PST area, so it's safe)
    if [ -n "$TEMP_CONFIG" ] && [ -f "$TEMP_CONFIG" ]; then
        rm -f "$TEMP_CONFIG" 2>/dev/null || true
        echo "Temporary config cleaned up"
    fi
    
    echo "Cleanup completed"
}

# Handle signals gracefully
handle_signal() {
    echo "Received signal, shutting down gracefully..."
    cleanup
    exit 0
}

# Set up signal traps
trap 'handle_signal' SIGTERM SIGINT SIGQUIT
trap 'cleanup' EXIT

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
        BUFFER_SIZE=$(stat -c%s "$BUFFER_FILE" 2>/dev/null || echo "unknown")
        echo "Existing buffer file size: $BUFFER_SIZE bytes"
        
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
    
    # Start the service in background so we can manage it properly
    echo "Starting MQTT buffer service..."
    echo "Binary: $SCRIPT_DIR/mqtt-buffer"
    echo "Config: $CONFIG_FILE"
    
    # Use background execution instead of exec
    "$SCRIPT_DIR/mqtt-buffer" &
    SERVICE_PID=$!
    
    echo "Service started with PID: $SERVICE_PID"
    
    # Wait for the service to complete
    wait "$SERVICE_PID"
    local exit_code=$?
    
    echo "Service exited with code: $exit_code"
    SERVICE_PID=""
    
    return $exit_code
}

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