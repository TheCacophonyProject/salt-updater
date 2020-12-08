package main

import (
	"encoding/json"
	"errors"
	"log"

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

//IsRunning will return true if a salt update is currently running
func (s service) IsRunning() (bool, *dbus.Error) {
	return s.saltUpdater.state.RunningUpdate, nil
}

//RunUpdate will start a salt update if one is not already running
func (s service) RunUpdate() *dbus.Error {
	s.saltUpdater.runSaltCall([]string{"state.apply", "--state-output=mixed"})
	return nil
}

//RunPing will send a test ping to the salt server
func (s service) RunPing() *dbus.Error {
	s.saltUpdater.runSaltCall([]string{"test.ping"})
	return nil
}

func (s service) RunPingSync() ([]byte, *dbus.Error) {
	state, err := s.saltUpdater.runSaltCallSync([]string{"test.ping"})
	saltJSON, err := json.Marshal(state)
	if err != nil {
		return nil, makeDbusError("RunPingSync", err)
	}
	return saltJSON, nil
}

//State will get the current state of the salt update
func (s service) State() ([]byte, *dbus.Error) {
	saltJSON, err := json.Marshal(s.saltUpdater.state)
	if err != nil {
		return nil, makeDbusError("State", err)
	}
	return saltJSON, nil
}

func makeDbusError(name string, err error) *dbus.Error {
	return &dbus.Error{
		Name: dbusName + name,
		Body: []interface{}{err.Error()},
	}
}
