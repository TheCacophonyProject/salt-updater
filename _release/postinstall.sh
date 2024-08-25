#!/bin/bash
systemctl daemon-reload
systemctl enable salt-updater.service
systemctl start salt-updater.service ## Don't restart as that can interfere with a salt update
salt-updater enable-auto-update
