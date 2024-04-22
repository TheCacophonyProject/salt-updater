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
	"errors"
	"fmt"
	"io/ioutil"
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
	"github.com/TheCacophonyProject/modemd/modemlistener"
	saltrequester "github.com/TheCacophonyProject/salt-updater"
	arg "github.com/alexflint/go-arg"
)

var version = "<not set>"

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
	if _, err := os.Stat("/etc/salt/minion_id"); os.IsNotExist(err) {
		log.Println("The salt minion_id file was not found, meaning that the device has not registered yet, exiting.")
		// return nil
	}

	if args.RunDbus {
		saltState, err := runDbus()
		if err != nil {
			return err
		}
		if saltState.AutoUpdate {
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
		return enableAutoUpdate()
	}

	if args.DisableAutoUpdate {
		return disableAutoUpdate()
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
	if err != nil {
		return nil, err
	}
	//
	salt := &saltUpdater{
		state: saltState,
	}
	go salt.modemConnectedListener()
	if err := startService(salt); err != nil {
		return saltState, err
	}
	// runtime.Goexit()
	return saltState, err
}

func (s *saltUpdater) runSaltCallSync(args []string, updateCall bool) (*saltrequester.SaltState, error) {
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
	if updateCall && s.state.LastCallSuccess {
		s.state.LastUpdate = time.Now()
	}
	nodegroupOut, err := ioutil.ReadFile("/etc/cacophony/salt-nodegroup")
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

func (s *saltUpdater) runSaltCall(args []string, makeEvent bool) {
	if s.state.RunningUpdate {
		return
	}
	go func(s *saltUpdater) {
		s.runSaltCallSync(args, makeEvent)
	}(s)
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

func enableAutoUpdate() error {
	saltState, err := saltrequester.ReadStateFile()
	if err != nil {
		return err
	}
	saltState.AutoUpdate = true
	err = saltrequester.WriteStateFile(saltState)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("auto update is enabled")
	return nil
}

func disableAutoUpdate() error {
	saltState, err := saltrequester.ReadStateFile()
	if err != nil {
		return err
	}
	saltState.AutoUpdate = false
	err = saltrequester.WriteStateFile(saltState)
	if os.IsNotExist(err) || err == nil {
		log.Println("auto update disabled")
		return nil
	}
	return err
}

func isAutoUpdateOn() (bool, error) {
	saltState, err := saltrequester.ReadStateFile()
	if err != nil {
		return false, err
	}
	return saltState.AutoUpdate, nil
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
		s.runSaltCall([]string{"test.ping"}, false)
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
