package saltrequester

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

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/godbus/dbus"
)

const (
	dbusPath   = "/org/cacophony/salt_helper"
	dbusDest   = "org.cacophony.salt_helper"
	methodBase = "org.cacophony.salt_helper"
)

var log = logging.NewLogger("info")

var nodeGroupToBranch = map[string]string{
	"tc2-dev":  "dev",
	"tc2-test": "test",
	"tc2-prod": "prod",
	"dev-pis":  "dev",
	"test-pis": "test",
	"prod-pis": "prod",
}

// SaltState holds info of the current state of salt
type SaltState struct {
	RunningUpdate            bool
	RunningArgs              []string
	LastCallOut              string
	LastCallSuccess          bool
	LastCallNodegroup        string
	LastCallArgs             []string
	LastUpdate               time.Time
	UpdateProgressPercentage int
	UpdateProgressStr        string
}

// IsRunning will return true if a salt update is running
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

// RunUpdate will run a salt update if one is not already running
func RunUpdate() error {
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".RunUpdate", 0).Store()
}

// RunUpdate will run a salt update if one is not already running
func ForceUpdate() error {
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".ForceUpdate", 0).Store()
}

// RunPing will ping the salt server if a salt call is not already running
func RunPing() error {
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".RunPing", 0).Store()
}

// RunPingSync will make a synchronous ping call to the server
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

// State will return the state of the salt update
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

func SetAutoUpdate(autoUpdate bool) error {
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".SetAutoUpdate", 0, autoUpdate).Store()
}

func IsAutoUpdateOn() (bool, error) {
	obj, err := getDbusObj()
	if err != nil {
		return false, err
	}
	var autoupdate bool
	if err := obj.Call(methodBase+".IsAutoUpdateOn", 0).Store(&autoupdate); err != nil {
		return false, err
	}
	return autoupdate, nil
}

func getDbusObj() (dbus.BusObject, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	obj := conn.Object(dbusDest, dbusPath)
	return obj, nil
}

const saltUpdateFile = "/etc/cacophony/saltUpdate.json"

// possibly need file locks??
func WriteStateFile(saltState *SaltState) error {

	saltStateJSON, err := json.Marshal(saltState)
	if err != nil {
		log.Printf("failed to marshal saltUpdater: %v\n", err)
		return err
	}
	err = os.WriteFile(saltUpdateFile, saltStateJSON, 0644)
	if err != nil {
		log.Printf("failed to save salt JSON to file: %v\n", err)
	}
	return err

}
func ReadStateFile() (*SaltState, error) {
	saltState := &SaltState{}

	if _, err := os.Stat(saltUpdateFile); errors.Is(err, os.ErrNotExist) {
		err = WriteStateFile(saltState)
		if err != nil {
			return saltState, err
		}
	}
	data, err := os.ReadFile(saltUpdateFile)
	if err != nil {
		log.Printf("error reading previous salt state: %v", err)
	} else if err := json.Unmarshal(data, saltState); err != nil {
		log.Printf("error loading previous salt state: %v", err)
	}
	return saltState, err
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
	saltState, _ := ReadStateFile()
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
