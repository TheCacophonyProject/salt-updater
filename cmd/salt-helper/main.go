/*
salt-updater - Runs salt updates
Copyright (C) 2018, The Cacophony Project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.
*/

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/go-utils/saltutil"
	"github.com/TheCacophonyProject/modemd/modemlistener"
	saltrequester "github.com/TheCacophonyProject/salt-updater"
	arg "github.com/alexflint/go-arg"
	"github.com/sirupsen/logrus"
)

var version = "<not set>"

var log *logrus.Logger

const configDir = goconfig.DefaultConfigDir
const minionLogFile = "/var/log/salt/minion"
const totalStatesCountFile = "/etc/cacophony/salt-states-count"

// Args app arguments
type Args struct {
	RunDbus           *subcommand          `arg:"subcommand:run-dbus" help:"Run the dbus service."`
	RunUpdate         *runUpdateSubcommand `arg:"subcommand:run-update" help:"Run a salt update if one is not already running."`
	Ping              *subcommand          `arg:"subcommand:ping" help:"Don't run a salt state.apply, just ping the salt server. Will not delay call."`
	State             *subcommand          `arg:"subcommand:state" help:"Print out the current state of the salt update"`
	EnableAutoUpdate  *subcommand          `arg:"subcommand:enable-auto-update" help:"Enables update check on PI boot up"`
	DisableAutoUpdate *subcommand          `arg:"subcommand:disable-auto-update" help:"Disables updates on PI boot"`
	CheckForUpdate    *subcommand          `arg:"subcommand:check-for-update" help:"Checks if there is an update available"`
	logging.LogArgs
}

type runUpdateSubcommand struct {
	Force bool `arg:"--force" help:"Force running an update even if it is already up to date."`
}

type subcommand struct{}

// Version return version of app
func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{}
	arg.MustParse(&args)
	return args
}

type saltUpdater struct {
	state *saltrequester.SaltState
}

var minionID string

func main() {
	if err := runMain(); err != nil {
		log.Fatal(err)
	}
}

func runMain() error {
	// Process arguments
	args := procArgs()

	// Setup logging
	log = logging.NewLogger(args.LogLevel)
	log.Printf("Running version: %s", version)

	// Read salt minion ID.
	// Exit if failed to read salt minion ID as it means the device is not yet ready to run salt.
	id, err := saltutil.GetMinionID(log)
	if err != nil {
		log.Error("Error reading minion ID: " + err.Error())
		return err
	}
	minionID = id
	log.Debug("Minion ID: " + minionID)

	// Read in salt config
	config, err := goconfig.New(configDir)
	if err != nil {
		return err
	}
	var saltSetup = goconfig.DefaultSalt()
	if err := config.Unmarshal(goconfig.SaltKey, &saltSetup); err != nil {
		return err
	}
	log.Printf("Salt config: %+v", saltSetup)

	// Run DBus service
	if args.RunDbus != nil {
		log.Info("Running dbus service")
		_, err := runDbus()
		if err != nil {
			return err
		}
		for {
			// Check for update every 24 hours
			err := saltrequester.RunUpdate()
			if err != nil {
				log.Error("Error running salt update: " + err.Error())
			}
			time.Sleep(24 * time.Hour)
		}
	}

	// Ping salt master
	if args.Ping != nil {
		log.Info("Calling salt ping")
		return saltrequester.RunPing()
	}

	// Check salt state
	if args.State != nil {
		state, err := saltrequester.State()
		if err != nil {
			return fmt.Errorf("failed to get salt state, %v", err)
		}
		log.Printf("salt state:\n%+v\n", *state)
		return nil
	}

	// Enable auto update
	if args.EnableAutoUpdate != nil {
		return setAutoUpdate(true)
	}

	// Disable auto update
	if args.DisableAutoUpdate != nil {
		return setAutoUpdate(false)
	}

	// Run salt update
	if args.RunUpdate != nil {
		var err error
		if args.RunUpdate.Force {
			log.Println("Forcing a salt update.")
			err = saltrequester.ForceUpdate()
		} else {
			log.Println("Calling for a salt update.")
			err = saltrequester.RunUpdate()
		}
		if err != nil {
			log.Println("Error calling for a salt update.")
			return err
		}
		return nil
	}

	if args.CheckForUpdate != nil {
		// Check for the nodegroup changing
		nodegroupChange, err := checkNodeGroupChange()
		if err != nil {
			log.Error(err)
			return err
		}
		if nodegroupChange {
			log.Info("Found nodegroup change, recommend a salt update.")
			return nil
		}

		// Log last time a update was run.
		state, err := saltrequester.State()
		nodegroup := state.LastCallNodegroup
		if err != nil {
			return fmt.Errorf("failed to get salt state, %v", err)
		}
		log.Printf("Last update was run at '%s', with nodegroup '%s'", state.LastUpdate.Format("2006-01-02 15:04:05"), nodegroup)

		// Log when the latest software was released.
		latestUpdateTime, err := GetLatestUpdateTime(nodegroup)
		if err != nil {
			log.Errorf("Error getting latest update time: %v", err)
			return err
		}
		log.Printf("Latest software update was published at '%s', for nodegroup '%s'", latestUpdateTime.Format("2006-01-02 15:04:05"), nodegroup)
		if state.LastUpdate.Before(*latestUpdateTime) {
			log.Info("Found new update, recommend a salt update.")
			return nil
		} else {
			log.Info("No new update found, nothing to do.")
		}

		return nil
	}

	log.Error("No command specified.")
	return errors.New("no command specified")
}

// checkNodeGroupChange checks if the node group is consistent between:
// - Salt grain file.
// - Salt state
// - /etc/cacophony/nodegroup
// Return true if any of them don't match with each other.
func checkNodeGroupChange() (bool, error) {
	// Get salt state nodegroup
	saltState, err := saltrequester.ReadStateFile()
	if err != nil {
		log.Errorf("Error reading salt state: %v", err)
		return false, err
	}
	stateNodeGroup := strings.TrimSpace(saltState.LastCallNodegroup)
	log.Debug("State nodegroup: " + stateNodeGroup)

	// Get nodegroup from /etc/cacophony/nodegroup
	fileNodeGroup, err := saltutil.GetNodegroupFromFile()
	if err != nil {
		log.Errorf("Error reading nodegroup file: %v", err)
		return false, err
	}
	log.Debug("File nodegroup: " + fileNodeGroup)

	// Get salt grain nodegroup
	grains, err := saltutil.GetSaltGrains(log)
	if err != nil {
		log.Errorf("Error reading salt grains: %v", err)
		return false, err
	}
	grainsNodeGroup, ok := grains["environment"]
	if !ok {
		log.Debug("No nodegroup found in grains")
	}
	log.Debug("Grains nodegroup: " + grainsNodeGroup)

	change := grainsNodeGroup != stateNodeGroup || grainsNodeGroup != fileNodeGroup
	return change, nil
}

func runDbus() (*saltrequester.SaltState, error) {
	//Read in previous state
	saltState, err := saltrequester.ReadStateFile()
	saltState.UpdateProgressPercentage = 0
	saltState.UpdateProgressStr = ""
	if err != nil {
		return nil, err
	}
	salt := &saltUpdater{
		state: saltState,
	}
	go salt.modemConnectedListener()
	if err := startService(salt); err != nil {
		return saltState, err
	}
	return saltState, err
}

func (s *saltUpdater) runSaltCallSync(args []string, updateCall bool, updateTime time.Time) (*saltrequester.SaltState, error) {
	// Don't want multiple calls running at the same time
	if s.state.RunningUpdate {
		return nil, errors.New("failed to run salt call as one is already running")
	}

	log.Printf("Starting salt call: %v", args)
	s.state.RunningUpdate = true
	s.state.RunningArgs = args
	out, err := exec.Command("salt-call", args...).CombinedOutput()
	s.state.RunningUpdate = false
	s.state.RunningArgs = nil
	log.Printf("Finished salt call: %v", args)

	s.state.LastCallSuccess = err == nil
	s.state.LastCallOut = string(out)
	if updateCall && s.state.LastCallSuccess && !updateTime.IsZero() {
		s.state.LastUpdate = updateTime
	}

	nodegroup, err := saltutil.GetNodegroupFromFile()
	if err != nil {
		log.Errorf("failed to read nodegroup file: %v", err)
		s.state.LastCallNodegroup = "error reading nodegroup"
	} else {
		s.state.LastCallNodegroup = nodegroup
	}
	s.state.LastCallArgs = args

	err = saltrequester.WriteStateFile(s.state)
	if err != nil {
		log.Printf("failed to save salt JSON to file: %v\n", err)
	}
	if updateCall {
		event, err := makeEventFromState(*s.state)
		if err != nil {
			return nil, err
		}
		return s.state, eventclient.AddEvent(*event)
	}
	return s.state, nil
}

func (s *saltUpdater) runSaltCall(args []string, updateCall bool, updateTime time.Time) {
	if s.state.RunningUpdate {
		return
	}
	go func(s *saltUpdater) {
		s.runSaltCallSync(args, updateCall, updateTime)
	}(s)
}

func trackUpdateProgress(s *saltUpdater, stop chan bool) {
	s.state.UpdateProgressPercentage = 0
	s.state.UpdateProgressStr = "Initializing update..."
	log.Println("Tracking salt update progress.")

	file, err := os.Open(minionLogFile)
	if err != nil {
		fmt.Printf("Error opening file: %v\n", err)
		return
	}
	defer file.Close()

	file.Seek(0, io.SeekEnd)
	reader := bufio.NewReader(file)
	stateRe := regexp.MustCompile(`INFO\s+\]\[\d+\] Running state \[(.*)\]`)

	// Read totalStates from /etc/cacophony/salt-states
	// totalStates is used to give an estimate percentage completion so doesn't need to be accurate
	totalStatesStr, err := os.ReadFile(totalStatesCountFile)
	if err != nil {
		log.Printf("Error reading totalStates: %v\n", err)
	}
	totalStates, err := strconv.Atoi(strings.TrimSpace(string(totalStatesStr)))
	if err != nil {
		log.Printf("Error parsing totalStates: %v\n", err)
		totalStates = 100 // Lets assume 100 if we can't get it
	}
	// Adding 5 more states in case there are more states than the last run
	totalStates += 5

	stateCount := 0
	for {
		// Loop until we get a signal to stop
		select {
		case <-stop:
			log.Println("Stopped tracking salt update progress.")
			// Save totalStates to file so can be reloaded on next run
			err = os.WriteFile(totalStatesCountFile, []byte(fmt.Sprintf("%d", stateCount)), 0644)
			if err != nil {
				log.Printf("Error writing totalStates: %v\n", err)
			}
			return
		default:
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// Process line
		if stateRe.MatchString(line) {
			matches := stateRe.FindStringSubmatch(line)
			if len(matches) != 2 {
				log.Printf("Failed to parse state line: %s", line)
				continue
			}

			stateCount++
			state := matches[1]
			log.Printf("Running %d/%d state: %s\n", stateCount, totalStates, state)
			s.state.UpdateProgressPercentage = 100 * stateCount / totalStates
			s.state.UpdateProgressStr = state
		}
	}
}

const saltrepoURL = "https://api.github.com/repos/TheCacophonyProject/saltops/commits"

var nodeGroupToBranch = map[string]string{
	"tc2-dev":  "dev",
	"tc2-test": "test",
	"tc2-prod": "prod",
	"dev-pis":  "dev",
	"test-pis": "test",
	"prod-pis": "prod",
}

func GetLatestUpdateTime(nodegroup string) (*time.Time, error) {
	// Check what branch the is used for this nodegroup
	branch, ok := nodeGroupToBranch[nodegroup]
	if !ok {
		err := fmt.Errorf("cant find a salt branch  mapping for %v nodegroup", nodegroup)
		log.Errorf(err.Error())
		return nil, err
	}

	// Make request to salt repo
	u, err := url.Parse(saltrepoURL)
	if err != nil {
		log.Errorf("Failed to parse salt repo URL: %v", err)
		return nil, err
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

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("Failed to send request: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("bad update status check %v from url %v", resp.StatusCode, u.String())
		log.Errorf(err.Error())
		return nil, err
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Failed to read body: %v", err)
		return nil, err
	}
	var details []interface{}
	err = json.Unmarshal(body, &details)
	if err != nil {
		log.Errorf("Failed to unmarshal body: %v", err)
		return nil, err
	}
	if len(details) == 0 {
		log.Printf("No updates exists for %v node group", nodegroup)
		return nil, nil
	}
	commitDate := details[0].(map[string]interface{})["commit"].(map[string]interface{})["author"].(map[string]interface{})["date"].(string)
	layout := "2006-01-02T15:04:05Z"
	updateTime, err := time.Parse(layout, commitDate)
	if err != nil {
		log.Errorf("Failed to parse commit date: %v", err)
		return nil, err
	}

	return &updateTime, nil
}

func (s *saltUpdater) CheckIfUpdateAvailable() bool {
	_, _, err := saltrequester.UpdateExists()
	return err == nil
}

func (s *saltUpdater) runUpdate(updateTime time.Time) {
	if s.state.RunningUpdate {
		log.Println("Already running salt update")
		return
	}

	stopTrackingUpdate := make(chan bool)
	defer func() { stopTrackingUpdate <- true }()
	go trackUpdateProgress(s, stopTrackingUpdate)

	_, err := s.runSaltCallSync([]string{"state.apply", "--state-output=mixed", "--output-diff"}, true, updateTime)
	if err != nil {
		log.Printf("error running salt update: %v", err)
		return
	}

	log.Println("Finished running salt update")
	s.state.UpdateProgressPercentage = 100
	s.state.UpdateProgressStr = "Finished update"
}

func makeEventFromState(state saltrequester.SaltState) (*eventclient.Event, error) {

	outLines := strings.Split(state.LastCallOut, "\n")

	var succeeded, changed, failed, runTime float64

	for _, line := range outLines {
		if strings.HasPrefix(line, "Succeeded:") {
			numbers := extractNumbers(line)
			if len(numbers) != 2 {
				return nil, errors.New("failed to parse output of salt update")
			}
			succeeded = numbers[0]
			changed = numbers[1]
		}
		if strings.HasPrefix(line, "Failed:") {
			numbers := extractNumbers(line)
			if len(numbers) != 1 {
				return nil, errors.New("failed to parse output of salt update")
			}
			failed = numbers[0]
		}
		if strings.HasPrefix(line, "Total run time:") {
			numbers := extractNumbers(line)
			if len(numbers) != 1 {
				return nil, errors.New("failed to parse output of salt update")
			}
			runTime = numbers[0]
		}
	}

	details := map[string]interface{}{
		"changed":   changed,
		"failed":    failed,
		"succeeded": succeeded,
		"nodegroup": state.LastCallNodegroup,
		"success":   state.LastCallSuccess,
		"args":      state.LastCallArgs,
		"minionID":  minionID,
	}

	// if some failed add more details
	if failed > 0 || !state.LastCallSuccess {
		details["out"] = state.LastCallOut
		details["runTime"] = runTime
	}

	event := &eventclient.Event{
		Timestamp: time.Now(),
		Details:   details,
		Type:      "salt-update",
	}
	return event, nil
}

func extractNumbers(str string) []float64 {
	re := regexp.MustCompile(`[-]?\d[\d,]*[\.]?[\d{2}]*`)
	numberStrings := re.FindAllString(str, -1)
	results := make([]float64, len(numberStrings))
	for i, numberString := range numberStrings {
		n, _ := strconv.ParseFloat(numberString, 64)
		results[i] = n
	}
	return results
}

func setAutoUpdate(enable bool) error {
	config, err := goconfig.New(configDir)
	if err != nil {
		return err
	}
	var saltSetup = goconfig.DefaultSalt()
	if err := config.Unmarshal(goconfig.SaltKey, &saltSetup); err != nil {
		return err
	}
	saltSetup.AutoUpdate = enable
	return config.Set(goconfig.SaltKey, &saltSetup)
}

func isAutoUpdateOn() (bool, error) {
	config, err := goconfig.New(configDir)
	if err != nil {
		return false, err
	}
	var saltSetup = goconfig.DefaultSalt()
	if err := config.Unmarshal(goconfig.SaltKey, &saltSetup); err != nil {
		return false, err
	}
	return saltSetup.AutoUpdate, nil
}

func (s *saltUpdater) modemConnectedListener() {
	modemConnectSignal, err := modemlistener.GetModemConnectedSignalListener()
	if err != nil {
		log.Println("Failed to get modem connected signal listener")
		return
	}
	for {
		// Empty modemConnectSignal channel so as to not trigger from old signals
		emptyChannel(modemConnectSignal)
		<-modemConnectSignal
		log.Println("Modem connected.")
		s.runSaltCall([]string{"test.ping"}, false, time.Now())
	}
}

func emptyChannel(ch chan time.Time) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
