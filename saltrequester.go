package saltrequester

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
	"os"
	"strings"

	"time"

	"github.com/godbus/dbus"
)

const (
	dbusPath   = "/org/cacophony/saltupdater"
	dbusDest   = "org.cacophony.saltupdater"
	methodBase = "org.cacophony.saltupdater"
)

// SaltState holds info of the current state of salt
type SaltState struct {
	RunningUpdate     bool
	RunningArgs       []string
	LastCallOut       string
	LastCallSuccess   bool
	LastCallNodegroup string
	LastCallArgs      []string
	LastUpdate        time.Time
	AutoUpdate        bool
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
	updateAvailable, err := updateExists()
	if err != nil {
		log.Printf("Error checking if update exists %v will run salt state", err)
	}
	//if we have an error lets just run salt update
	if err == nil && !updateAvailable {
		log.Println("No update available")
		return nil
	}

	log.Printf("Running state apply")
	obj, err := getDbusObj()
	if err != nil {
		return err
	}
	return obj.Call(methodBase+".RunUpdate", 0).Store()
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
	data, err := ioutil.ReadFile(saltUpdateFile)
	if err != nil {
		log.Printf("error reading previous salt state: %v", err)
	} else if err := json.Unmarshal(data, saltState); err != nil {
		log.Printf("error loading previous salt state: %v", err)
	}
	return saltState, err
}

// updateExists checks if there has been any git updates since the last update time for this minions nodegroup
// uses github api to view last commit to the repo
func updateExists() (bool, error) {
	nodegroupOut, err := ioutil.ReadFile("/etc/cacophony/salt-nodegroup")
	nodeGroup := string(nodegroupOut)
	nodeGroup = strings.TrimSuffix(nodeGroup, "\n")
	hyphenIndex := strings.Index(nodeGroup, "-")
	if hyphenIndex != -1 {
		nodeGroup = nodeGroup[hyphenIndex+1:]
	}

	saltState, _ := ReadStateFile()
	log.Printf("Checking for updates for saltops %v branch, last update was %v", nodeGroup, saltState.LastUpdate)

	const saltrepoURL = "https://api.github.com/repos/TheCacophonyProject/saltops/commits"
	u, err := url.Parse(saltrepoURL)
	if err != nil {
		return false, err
	}
	params := url.Values{}
	params.Add("sha", nodeGroup)
	params.Add("per_page", "1")

	u.RawQuery = params.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
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
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("Bad update status check %v from url %v", resp.StatusCode, u.String())
	}
	body, err := io.ReadAll(resp.Body)
	var details []interface{}
	err = json.Unmarshal(body, &details)
	if err != nil {
		return false, err
	}
	if len(details) == 0 {
		log.Printf("No updates exists for %v node group", nodegroupOut)
		return false, nil
	}
	commitDate := details[0].(map[string]interface{})["commit"].(map[string]interface{})["author"].(map[string]interface{})["date"].(string)
	layout := "2006-01-02T15:04:05Z"
	t, err := time.Parse(layout, commitDate)

	return t.After(saltState.LastUpdate), nil
}
