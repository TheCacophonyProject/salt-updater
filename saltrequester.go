package saltrequester

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"time"

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/godbus/dbus"
)

const (
	dbusPath       = "/org/cacophony/salt_helper"
	dbusDest       = "org.cacophony.salt_helper"
	methodBase     = "org.cacophony.salt_helper"
	saltVersionUrl = "https://raw.githubusercontent.com/TheCacophonyProject/salt-version-info/refs/heads/main/salt-version-info.json"
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

func UpdateExists() (bool, time.Time, error) {
	nodegroupOut, err := os.ReadFile("/etc/cacophony/salt-nodegroup")
	if err != nil {
		return false, time.Time{}, err
	}
	return UpdateExistsForNodeGroup(string(nodegroupOut))
}

// UpdateExists checks if there has been any git updates since the last update time for this minions nodegroup
// uses github api to view last commit to the repo
func UpdateExistsForNodeGroup(nodeGroup string) (bool, time.Time, error) {

	nodeGroup = strings.TrimSuffix(nodeGroup, "\n")
	branch, ok := nodeGroupToBranch[nodeGroup]
	var updateTime time.Time

	if !ok {
		return false, updateTime, fmt.Errorf("cant find a salt branch  mapping for %v nodegroup", nodeGroup)
	}
	saltState, _ := ReadStateFile()
	log.Printf("Checking for updates for saltops %v branch, last update was %v", branch, saltState.LastUpdate)
	resp, err := http.Get(saltVersionUrl)

	if err != nil {
		return false, updateTime, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, updateTime, fmt.Errorf("bad update status check %v from url %v", resp.StatusCode, saltVersionUrl)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, updateTime, err

	}
	var details map[string]interface{}
	err = json.Unmarshal(body, &details)
	if err != nil {
		return false, updateTime, err
	}

	var commitDate string
	if branchDetails, ok := details[branch]; ok {
		if tc2, ok := branchDetails.(map[string]interface{})["tc2"]; ok {
			if commitDate, ok = tc2.(map[string]interface{})["commitDate"].(string); !ok {
				err = fmt.Errorf("Could not find commitDate key in json %v", commitDate)
			}
		} else {
			err = fmt.Errorf("Could not find tc2 key in json %v", branchDetails)
		}
	} else {
		err = fmt.Errorf("Could not find %v key in json %v", branch, details)
	}
	if err != nil {
		return false, updateTime, err
	}
	layout := "2006-01-02T15:04:05Z"
	updateTime, err = time.Parse(layout, commitDate)
	if err != nil {
		return false, updateTime, err
	}

	return updateTime.After(saltState.LastUpdate), updateTime, nil
}
