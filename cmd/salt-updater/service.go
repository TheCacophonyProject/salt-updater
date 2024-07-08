package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	saltrequester "github.com/TheCacophonyProject/salt-updater"
	"github.com/godbus/dbus"
	"github.com/godbus/dbus/introspect"
)

var nodeGroupToBranch = map[string]string{
	"tc2-dev":  "dev",
	"tc2-test": "test",
	"tc2-prod": "prod",
	"dev-pis":  "dev",
	"test-pis": "test",
	"prod-pis": "prod",
}

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
	updateAvailable, updateTime, err := UpdateExists()
	if err != nil {
		log.Printf("Error checking if update exists %v will run salt state", err)
	}
	//if we have an error lets just run salt update
	if err == nil && !updateAvailable {
		log.Println("No update available")
		return nil
	}

	go s.saltUpdater.runUpdate(updateTime)
	return nil
}

// UpdateExists checks if there has been any git updates since the last update time for this minions nodegroup
// uses github api to view last commit to the repo
func UpdateExists() (bool, time.Time, error) {

	nodegroupOut, err := ioutil.ReadFile("/etc/cacophony/salt-nodegroup")
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
