#!/bin/bash

set -e

echo "MQTT Buffer Service - PiKVM Deployment Script"
echo "============================================="

# Configuration
SERVICE_NAME="mqtt-buffer"
INSTALL_DIR="/opt/mqtt-buffer"
SERVICE_USER="root"
SERVICE_GROUP="kvmd-pst"

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root (use sudo)"
    exit 1
fi

# Check if we're running on PiKVM
if ! command -v kvmd-pstrun &> /dev/null; then
    echo "Warning: kvmd-pstrun not found. This script is designed for PiKVM systems."
    read -p "Continue anyway? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

echo "Creating directories..."
mkdir -p $INSTALL_DIR
chmod 755 $INSTALL_DIR

echo "Building application..."
go build -ldflags="-s -w" -o $INSTALL_DIR/$SERVICE_NAME main.go

echo "Installing files..."
cp pikvm-wrapper.sh $INSTALL_DIR/
cp config.json $INSTALL_DIR/
chmod +x $INSTALL_DIR/pikvm-wrapper.sh
chmod +x $INSTALL_DIR/$SERVICE_NAME

echo "Setting permissions..."
chown -R $SERVICE_USER:$SERVICE_GROUP $INSTALL_DIR

echo "Installing systemd service..."
cp mqtt-buffer-pikvm.service /etc/systemd/system/mqtt-buffer.service
systemctl daemon-reload

echo "Testing PST access..."
if command -v kvmd-pstrun &> /dev/null; then
    echo "Testing persistent storage access..."
    kvmd-pstrun -- /bin/bash -c 'echo "PST test: $(date)" > $KVMD_PST_DATA/mqtt-buffer-test.txt && cat $KVMD_PST_DATA/mqtt-buffer-test.txt'
    echo "PST test completed successfully"
else
    echo "Skipping PST test (kvmd-pstrun not available)"
fi

echo "Enabling and starting service..."
systemctl enable mqtt-buffer
systemctl restart mqtt-buffer

echo "Waiting for service to start..."
sleep 3

echo "Checking service status..."
if systemctl is-active --quiet mqtt-buffer; then
    echo "✅ Service is running successfully"
    systemctl status mqtt-buffer --no-pager -l
else
    echo "❌ Service failed to start"
    systemctl status mqtt-buffer --no-pager -l
    echo ""
    echo "Check logs with: journalctl -u mqtt-buffer -f"
    exit 1
fi

echo ""
echo "🎉 PiKVM MQTT Buffer Service Deployment Complete!"
echo "================================================="
echo ""
echo "Service management:"
echo "  Status:  systemctl status mqtt-buffer"
echo "  Logs:    journalctl -u mqtt-buffer -f"
echo "  Stop:    systemctl stop mqtt-buffer"
echo "  Start:   systemctl start mqtt-buffer"
echo "  Restart: systemctl restart mqtt-buffer"
echo ""
echo "Configuration file: $INSTALL_DIR/config.json"
echo "Buffer storage: PiKVM Persistent Storage (/var/lib/kvmd/pst/data/)"
echo ""
echo "🔧 PiKVM-Specific Notes:"
echo "- Buffer data is stored in PiKVM's persistent storage (256MiB limit)"
echo "- Service runs through kvmd-pstrun for proper storage access"
echo "- Reduced buffer size and retention for embedded system efficiency"
echo "- Service automatically starts after PiKVM services"
echo ""
echo "To check buffer statistics:"
echo "  journalctl -u mqtt-buffer --since '1 hour ago' | grep 'Buffer stats'"
