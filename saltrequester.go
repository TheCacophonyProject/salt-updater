package saltrequester

import (
	"encoding/json"
	"log"

	"github.com/godbus/dbus"
)

const (
	dbusPath   = "/org/cacophony/saltupdater"
	dbusDest   = "org.cacophony.saltupdater"
	methodBase = "org.cacophony.saltupdater"
)

//SaltState holds info of the current state of salt
type SaltState struct {
	RunningUpdate     bool
	RunningArgs       []string
	LastCallOut       string
	LastCallSuccess   bool
	LastCallNodegroup string
	LastCallArgs      []string
}

//IsRunning will return true if a salt update is running
func IsRunning() (bool, error) {
	obj, err := getDbusObj()
	if err != nil {
		return false, err
	}
	if err := obj.Call(methodBase+".IsRunning", 0).Store(); err != nil {
		return false, err
	}

	return false, nil
}

//RunUpdate will run a salt update if one is not already running
func RunUpdate() error {
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".RunUpdate", 0).Store()
}

//RunPing will ping the salt server if a salt call is not already running
func RunPing() error {
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".RunPing", 0).Store()
}

//RunPingSync will make a synchronous ping call to the server
func RunPingSync() (*SaltState, error) {
	obj, err := getDbusObj()
	if err != nil {
		return nil, err
	}
	stateBytes := []byte{}
	if err := obj.Call(methodBase+".RunPingSync", 0).Store(&stateBytes); err != nil {
		return nil, err
	}
	state := &SaltState{}
	if err := json.Unmarshal(stateBytes, state); err != nil {
		log.Println("failed to unmarshal SaltState")
		return nil, err
	}
	return state, nil
}

//State will return the state of the salt update
func State() (*SaltState, error) {
	obj, err := getDbusObj()
	if err != nil {
		return nil, err
	}
	stateBytes := []byte{}
	if err := obj.Call(methodBase+".State", 0).Store(&stateBytes); err != nil {
		return nil, err
	}
	state := &SaltState{}
	if err := json.Unmarshal(stateBytes, state); err != nil {
		log.Println("failed to unmarshal SaltState")
		return nil, err
	}
	return state, nil
}

func getDbusObj() (dbus.BusObject, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	obj := conn.Object(dbusDest, dbusPath)
	return obj, nil
}

func sendOnRequest() error {
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".StayOn", 0).Store()
}
