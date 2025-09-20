#!/bin/bash

set -e

echo "MQTT Buffer Service Deployment Script"
echo "====================================="

# Configuration
SERVICE_NAME="mqtt-buffer"
INSTALL_DIR="/opt/mqtt-buffer"
DATA_DIR="/var/lib/mqtt-buffer"
SERVICE_USER="mqtt-buffer"
SERVICE_GROUP="mqtt-buffer"

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root (use sudo)"
    exit 1
fi

echo "Creating service user and group..."
if ! getent group $SERVICE_GROUP > /dev/null 2>&1; then
    groupadd --system $SERVICE_GROUP
fi

if ! getent passwd $SERVICE_USER > /dev/null 2>&1; then
    useradd --system --gid $SERVICE_GROUP --home-dir $DATA_DIR \
            --shell /usr/sbin/nologin --comment "MQTT Buffer Service" $SERVICE_USER
fi

echo "Creating directories..."
mkdir -p $INSTALL_DIR
mkdir -p $DATA_DIR
mkdir -p /var/log/$SERVICE_NAME

echo "Setting permissions..."
chown -R $SERVICE_USER:$SERVICE_GROUP $INSTALL_DIR
chown -R $SERVICE_USER:$SERVICE_GROUP $DATA_DIR
chown -R $SERVICE_USER:$SERVICE_GROUP /var/log/$SERVICE_NAME

echo "Building application..."
go build -o $INSTALL_DIR/$SERVICE_NAME main.go

echo "Installing configuration..."
cp config.json $INSTALL_DIR/
chown $SERVICE_USER:$SERVICE_GROUP $INSTALL_DIR/config.json

echo "Installing systemd service..."
cp $SERVICE_NAME.service /etc/systemd/system/
systemctl daemon-reload

echo "Enabling and starting service..."
systemctl enable $SERVICE_NAME
systemctl restart $SERVICE_NAME

echo "Checking service status..."
sleep 2
systemctl status $SERVICE_NAME --no-pager

echo ""
echo "Deployment complete!"
echo "==================="
echo "Service status: systemctl status $SERVICE_NAME"
echo "Service logs: journalctl -u $SERVICE_NAME -f"
echo "Buffer file: $DATA_DIR/mqtt-buffer.json"
echo ""
echo "To check buffer statistics:"
echo "journalctl -u $SERVICE_NAME --since '1 hour ago' | grep 'Buffer stats'"
