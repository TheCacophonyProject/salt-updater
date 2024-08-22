package main

import (
	"encoding/json"
	"errors"
	"log"
	"time"

	saltrequester "github.com/TheCacophonyProject/salt-updater"
	"github.com/godbus/dbus"
	"github.com/godbus/dbus/introspect"
)

const (
	dbusName = "org.cacophony.saltupdater"
	dbusPath = "/org/cacophony/saltupdater"
)

type service struct {
	saltUpdater *saltUpdater
}

func startService(salt *saltUpdater) error {
	log.Println("starting dbus service")
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	reply, err := conn.RequestName(dbusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("dbus names already taken")
	}

	s := &service{
		saltUpdater: salt,
	}
	conn.Export(s, dbusPath, dbusName)
	conn.Export(genIntrospectable(s), dbusPath, "org.freedesktop.DBus.Introspectable")
	return nil
}

func genIntrospectable(v interface{}) introspect.Introspectable {
	node := &introspect.Node{
		Interfaces: []introspect.Interface{{
			Name:    dbusName,
			Methods: introspect.Methods(v),
		}},
	}
	return introspect.NewIntrospectable(node)
}

// IsRunning will return true if a salt update is currently running
func (s service) IsRunning() (bool, *dbus.Error) {
	return s.saltUpdater.state.RunningUpdate, nil
}

// RunUpdate will start a salt update if one is not already running
func (s service) RunUpdate() *dbus.Error {
	updateAvailable, updateTime, err := saltrequester.UpdateExists()
	if err != nil {
		log.Printf("Error checking if update exists %v will run salt state", err)
	}
	//if we have an error lets just run salt update
	if err == nil && !updateAvailable {
		s.saltUpdater.state.UpdateProgressPercentage = 100
		s.saltUpdater.state.UpdateProgressStr = "No update available"
		log.Println("No update available")
		return nil
	}

	go s.saltUpdater.runUpdate(updateTime)
	return nil
}

func (s service) ForceUpdate() *dbus.Error {
	go s.saltUpdater.runUpdate(time.Now())
	return nil
}

// RunPing will send a test ping to the salt server
func (s service) RunPing() *dbus.Error {
	s.saltUpdater.runSaltCall([]string{"test.ping"}, false, time.Now())
	return nil
}

func (s service) RunPingSync() ([]byte, *dbus.Error) {
	state, err := s.saltUpdater.runSaltCallSync([]string{"test.ping"}, false, time.Now())
	if err != nil {
		return nil, makeDbusError("RunPingSync", err)
	}
	saltJSON, err := json.Marshal(state)
	if err != nil {
		return nil, makeDbusError("RunPingSync", err)
	}
	return saltJSON, nil
}

// State will get the current state of the salt update
func (s service) State() ([]byte, *dbus.Error) {
	saltJSON, err := json.Marshal(s.saltUpdater.state)
	if err != nil {
		return nil, makeDbusError("State", err)
	}
	return saltJSON, nil
}

func (s service) SetAutoUpdate(autoUpdate bool) *dbus.Error {
	err := setAutoUpdate(autoUpdate)

	if err != nil {
		makeDbusError("SetAutoUpdate", err)
	}
	return nil
}

func (s service) IsAutoUpdateOn() (bool, *dbus.Error) {
	autoUpdate, err := isAutoUpdateOn()
	if err != nil {
		return false, makeDbusError("IsAutoUpdateOn", err)
	}
	return autoUpdate, nil
}

func makeDbusError(name string, err error) *dbus.Error {
	return &dbus.Error{
		Name: dbusName + "." + name,
		Body: []interface{}{err.Error()},
	}
}
