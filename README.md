# salt-updater

`salt-updater` runs a dbus service to make salt calls. After install a Cron job is setup to run at 23:00 every night and will wait between 0 to 120 minutes before running a salt update. This is supposed to replace the salt-auto-update service that is running on the salt server.

`salt-updater` has the go library `saltrequester` to help making salt calls through go.