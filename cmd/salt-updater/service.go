package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	saltrequester "github.com/TheCacophonyProject/salt-updater"
	"github.com/godbus/dbus"
	"github.com/godbus/dbus/introspect"
)

const (
	oldDbusName = "org.cacophony.saltupdater"
	oldDbusPath = "/org/cacophony/saltupdater"

	newDbusName = "org.cacophony.salt_helper"
	newDbusPath = "/org/cacophony/salt_helper"
)

type service struct {
	dbusName    string
	saltUpdater *saltUpdater
}

func startService(salt *saltUpdater) error {
	log.Println("Starting dbus service.")
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}

	replyOld, err := conn.RequestName(oldDbusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if replyOld != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("old dbus name already taken")
	}

	replyNew, err := conn.RequestName(newDbusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if replyNew != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("new dbus name already taken")
	}

	oldService := &service{
		dbusName:    oldDbusName,
		saltUpdater: salt,
	}

	newService := &service{
		dbusName:    newDbusName,
		saltUpdater: salt,
	}

	// Migrating to a new dbus path/name, so for now will support both
	// Export service on the old dbus path/name
	conn.Export(oldService, oldDbusPath, oldDbusName)
	conn.Export(genIntrospectable(oldService, oldDbusName), oldDbusPath, "org.freedesktop.DBus.Introspectable")

	// Export service on the new dbus path/name
	conn.Export(newService, newDbusPath, newDbusName)
	conn.Export(genIntrospectable(newService, newDbusName), newDbusPath, "org.freedesktop.DBus.Introspectable")

	return nil
}

func genIntrospectable(v interface{}, dbusName string) introspect.Introspectable {
	node := &introspect.Node{
		Interfaces: []introspect.Interface{{
			Name:    dbusName,
			Methods: introspect.Methods(v),
		}},
	}
	return introspect.NewIntrospectable(node)
}

func (s *service) CheckIfUsingOldDbus() {
	if s.dbusName == oldDbusName {
		log.Printf("Using old dbus name '%s', please use the new dbus name '%s'", oldDbusName, newDbusName)
	}
}

// IsRunning will return true if a salt update is currently running
func (s service) IsRunning() (bool, *dbus.Error) {
	s.CheckIfUsingOldDbus()
	return s.saltUpdater.state.RunningUpdate, nil
}

func (s service) RunUpdate() *dbus.Error {
	s.CheckIfUsingOldDbus()

	updateAvailable, updateTime, err := UpdateExists()
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
	s.CheckIfUsingOldDbus()
	go s.saltUpdater.runUpdate(time.Now())
	return nil
}

// UpdateExists checks if there has been any git updates since the last update time for this minions nodegroup
// uses github api to view last commit to the repo
func UpdateExists() (bool, time.Time, error) {

	nodegroupOut, err := os.ReadFile("/etc/cacophony/salt-nodegroup")
	if err != nil {
		return false, time.Time{}, err
	}
	nodeGroup := string(nodegroupOut)
	nodeGroup = strings.TrimSuffix(nodeGroup, "\n")
	branch, ok := nodeGroupToBranch[nodeGroup]
	var updateTime time.Time

	if !ok {
		return false, updateTime, fmt.Errorf("cant find a salt branch  mapping for %v nodegroup", nodegroupOut)
	}
	saltState, _ := saltrequester.ReadStateFile()
	log.Printf("Checking for updates for saltops %v branch, last update was %v", branch, saltState.LastUpdate)

	const saltrepoURL = "https://api.github.com/repos/TheCacophonyProject/saltops/commits"
	u, err := url.Parse(saltrepoURL)
	if err != nil {
		return false, updateTime, err
	}
	params := url.Values{}
	params.Add("sha", branch)
	params.Add("per_page", "1")

	u.RawQuery = params.Encode()

	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          5,
			IdleConnTimeout:       90 * time.Second,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, updateTime, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, updateTime, fmt.Errorf("bad update status check %v from url %v", resp.StatusCode, u.String())
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, updateTime, err

	}
	var details []interface{}
	err = json.Unmarshal(body, &details)
	if err != nil {
		return false, updateTime, err
	}
	if len(details) == 0 {
		log.Printf("No updates exists for %v node group", nodegroupOut)
		return false, updateTime, nil
	}
	commitDate := details[0].(map[string]interface{})["commit"].(map[string]interface{})["author"].(map[string]interface{})["date"].(string)
	layout := "2006-01-02T15:04:05Z"
	updateTime, err = time.Parse(layout, commitDate)
	if err != nil {
		return false, updateTime, err
	}

	return updateTime.After(saltState.LastUpdate), updateTime, nil
}

// RunPing will send a test ping to the salt server
func (s service) RunPing() *dbus.Error {
	s.CheckIfUsingOldDbus()
	s.saltUpdater.runSaltCall([]string{"test.ping"}, false, time.Now())
	return nil
}

func (s service) RunPingSync() ([]byte, *dbus.Error) {
	s.CheckIfUsingOldDbus()
	state, err := s.saltUpdater.runSaltCallSync([]string{"test.ping"}, false, time.Now())
	if err != nil {
		return nil, makeDbusError("RunPingSync", s.dbusName, err)
	}
	saltJSON, err := json.Marshal(state)
	if err != nil {
		return nil, makeDbusError("RunPingSync", s.dbusName, err)
	}
	return saltJSON, nil
}

// State will get the current state of the salt update
func (s service) State() ([]byte, *dbus.Error) {
	s.CheckIfUsingOldDbus()
	saltJSON, err := json.Marshal(s.saltUpdater.state)
	if err != nil {
		return nil, makeDbusError("State", s.dbusName, err)
	}
	return saltJSON, nil
}

func (s service) SetAutoUpdate(autoUpdate bool) *dbus.Error {
	s.CheckIfUsingOldDbus()
	err := setAutoUpdate(autoUpdate)

	if err != nil {
		return makeDbusError("SetAutoUpdate", s.dbusName, err)
	}
	return nil
}

func (s service) IsAutoUpdateOn() (bool, *dbus.Error) {
	s.CheckIfUsingOldDbus()
	autoUpdate, err := isAutoUpdateOn()
	if err != nil {
		return false, makeDbusError("IsAutoUpdateOn", s.dbusName, err)
	}
	return autoUpdate, nil
}

func makeDbusError(name, dbusName string, err error) *dbus.Error {
	return &dbus.Error{
		Name: dbusName + "." + name,
		Body: []interface{}{err.Error()},
	}
}
