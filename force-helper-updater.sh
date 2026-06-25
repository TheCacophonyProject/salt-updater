#!/bin/bash
# this is a script to fix an issue in salt-updater 0.9.0 which may cause salt updates to not occur
if sudo salt-helper state 2>&1 | grep -q "RunningUpdate:false"; then
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
else
    echo "Update is running so not forcing salt-updater update"
fi