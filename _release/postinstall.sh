#!/bin/bash
systemctl daemon-reload
systemctl enable salt-updater.service
systemctl start salt-updater.service ## Don't restart as that can interfere with a salt update
chmod 600 /etc/cron.d/salt-updater