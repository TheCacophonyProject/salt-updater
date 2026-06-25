#!/bin/bash

if sudo salt-helper state 2>&1 | grep -q "RunningUpdate:false" && [ $(date -d "$(cat /etc/cacophony/last-salt-update)" +%s) -gt $(date -d "2026-06-25T00:00:00+12:00" +%s) ]; then
    result=True
    echo "Not running update"

    version=$(dpkg -s tc2-agent | grep '^Version:' | awk '{print $2}')
    echo "Tc2 agent version is $version"
    if [ "$version" != "0.9.0" ]; then
        echo "Update salt updater to 0.9.1"
        wget https://github.com/TheCacophonyProject/salt-updater/releases/download/v0.9.1/salt-updater_0.9.1_arm64.deb
        sudo dpkg -i salt-updater_0.9.1_arm64.deb
        rm salt-updater_0.9.1_arm64.deb
    fi
fi