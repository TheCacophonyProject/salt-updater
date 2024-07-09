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
	"errors"
	"fmt"
	"io"
	"math/rand"
	"runtime"

	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/modemd/modemlistener"
	saltrequester "github.com/TheCacophonyProject/salt-updater"
	arg "github.com/alexflint/go-arg"
)

var version = "<not set>"

const configDir = goconfig.DefaultConfigDir
const minionLogFile = "/var/log/salt/minion"
const totalStatesCountFile = "/etc/cacophony/salt-states-count"
const nodegroupFile = "/etc/cacophony/salt-nodegroup"
const minionIdFile = "/etc/salt/minion_id"

// Args app arguments
type Args struct {
	RunDbus            bool `arg:"--run-dbus" help:"Run the dbus service."`
	RandomDelayMinutes int  `arg:"--random-delay-minutes" help:"Delay update between 0 and given minutes."`
	Ping               bool `arg:"--ping" help:"Don't run a salt state.apply, just ping the salt server. Will not delay call."`
	State              bool `arg:"--state" help:"Print out the current state of the salt update"`
	EnableAutoUpdate   bool `arg:"--enable-auto-update" help:"Enables update check on PI boot up"`
	DisableAutoUpdate  bool `arg:"--disable-auto-update" help:"Disables updates on PI boot"`
}

// Version return version of app
func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{
		RunDbus:            false,
		RandomDelayMinutes: 120,
	}
	arg.MustParse(&args)
	return args
}

func main() {
	if err := runMain(); err != nil {
		log.Fatal(err)
	}
}

type saltUpdater struct {
	state *saltrequester.SaltState
}

func runMain() error {
	log.SetFlags(0)
	args := procArgs()
	log.Printf("Running version: %s", version)

	// Don't want to run any salt commands before the device is registered as it will set a salt minion_id
	if _, err := os.Stat(minionIdFile); os.IsNotExist(err) {
		log.Println("The salt minion_id file was not found, meaning that the device has not registered yet, exiting.")
		return nil
	}
	saltState, _ := saltrequester.ReadStateFile()
	nodegroupOut, _ := os.ReadFile(nodegroupFile)
	nodegroup := strings.TrimSpace(string(nodegroupOut))
	if strings.TrimSpace(saltState.LastCallNodegroup) != nodegroup {
		log.Println("Node group has changed resetting last update time")
		saltState = &saltrequester.SaltState{LastCallNodegroup: nodegroup}
		err := saltrequester.WriteStateFile(saltState)
		if err != nil {
			return err
		}
	}
	config, err := goconfig.New(configDir)
	if err != nil {
		return err
	}
	var saltSetup = goconfig.DefaultSalt()
	if err := config.Unmarshal(goconfig.SaltKey, &saltSetup); err != nil {
		return err
	}
	log.Printf("Auto update is %v", saltSetup.AutoUpdate)
	if args.RunDbus {
		_, err := runDbus()
		if err != nil {
			return err
		}
		if saltSetup.AutoUpdate {
			saltrequester.RunUpdate()
		}
		runtime.Goexit()
	}

	if args.Ping {
		log.Println("calling salt ping")
		return saltrequester.RunPing()
	}

	if args.State {
		state, err := saltrequester.State()
		if err != nil {
			return fmt.Errorf("failed to get salt state, %v", err)
		}
		log.Printf("salt state:\n%+v\n", *state)
		return nil
	}

	if args.EnableAutoUpdate {
		return setAutoUpdate(true)
	}

	if args.DisableAutoUpdate {
		return setAutoUpdate(false)
	}

	minutes := rand.Intn(args.RandomDelayMinutes + 1)
	log.Printf("waiting %v minutes before running salt update\n", minutes)
	time.Sleep(time.Duration(minutes) * time.Minute)

	log.Println("calling salt update")
	return saltrequester.RunUpdate()
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
	if s.state.RunningUpdate {
		return nil, errors.New("failed to run salt call as one is already running")
	}
	s.state.RunningUpdate = true
	log.Printf("starting salt call: %v", args)
	s.state.RunningArgs = args
	out, err := exec.Command("salt-call", args...).Output()
	s.state.RunningUpdate = false
	s.state.RunningArgs = nil
	log.Println("finished salt call")
	s.state.LastCallSuccess = err == nil
	s.state.LastCallOut = string(out)
	if updateCall && s.state.LastCallSuccess && !updateTime.IsZero() {
		s.state.LastUpdate = updateTime
	}
	nodegroupOut, err := os.ReadFile(nodegroupFile)
	if err != nil {
		s.state.LastCallNodegroup = "error reading nodegroup"
	} else {
		s.state.LastCallNodegroup = strings.TrimSpace(string(nodegroupOut)) //Removes newline character
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
