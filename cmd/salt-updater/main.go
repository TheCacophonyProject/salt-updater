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
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	saltrequester "github.com/TheCacophonyProject/salt-updater"
	arg "github.com/alexflint/go-arg"
)

var version = "<not set>"

const saltUpdateFile = "/etc/cacophony/saltUpdate.json"
const autoUpdateCronPath = "/etc/cron.d/salt-updater"
const autoUpdateCronString = `#Run update every night at 23:00. By default salt-updater will wait between 0 and 120 minutes before running the update
0 23 * * * root /usr/bin/salt-updater
`

// Args app arguments
type Args struct {
	RunDbus            bool `arg:"--run-dbus" help:"Run the dbus service."`
	RandomDelayMinutes int  `arg:"--random-delay-minutes" help:"Delay update between 0 and given minutes."`
	Ping               bool `arg:"--ping" help:"Don't run a salt state.apply, just ping the salt server. Will not delay call."`
	State              bool `arg:"--state" help:"Print out the current state of the salt update"`
	EnableAutoUpdate   bool `arg:"--enable-auto-update" help:"Enables cron job to run update every night."`
	DisableAutoUpdate  bool `arg:"--disable-auto-update" help:"Disables cron job to run update every night."`
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
	rand.Seed(time.Now().UnixNano())
	args := procArgs()
	log.Printf("running version: %s", version)

	// Check if minion_id file is present
	if _, err := os.Stat("/etc/salt/minion_id"); err != nil {
		log.Println("Salt minion_id id file is not present, exiting.")
		return nil
	}

	if args.RunDbus {
		return runDbus()
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

func runDbus() error {
	//Read in previous state
	saltState := &saltrequester.SaltState{}
	data, err := ioutil.ReadFile(saltUpdateFile)
	if err != nil {
		log.Printf("error reading previous salt state: %v", err)
	} else if err := json.Unmarshal(data, saltState); err != nil {
		log.Printf("error loading previous salt state: %v", err)
	}

	salt := &saltUpdater{
		state: saltState,
	}
	if err := startService(salt); err != nil {
		return err
	}
	runtime.Goexit()
	return nil
}

func (s *saltUpdater) runSaltCallSync(args []string, makeEvent bool) (*saltrequester.SaltState, error) {
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
	nodegroupOut, err := ioutil.ReadFile("/etc/cacophony/salt-nodegroup")
	if err != nil {
		s.state.LastCallNodegroup = "error reading nodegroup"
	} else {
		s.state.LastCallNodegroup = strings.TrimSpace(string(nodegroupOut)) //Removes newline character
	}
	s.state.LastCallArgs = args
	saltStateJSON, err := json.Marshal(*s.state)
	if err != nil {
		log.Printf("failed to marshal saltUpdater: %v\n", err)
		return nil, err
	}
	err = ioutil.WriteFile(saltUpdateFile, saltStateJSON, 0644)
	if err != nil {
		log.Printf("failed to save salt JSON to file: %v\n", err)
	}
	if makeEvent {
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
	err := ioutil.WriteFile(autoUpdateCronPath, []byte(autoUpdateCronString), 0600)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("auto update is enabled")
	return nil
}

func disableAutoUpdate() error {
	err := os.Remove(autoUpdateCronPath)
	if os.IsNotExist(err) || err == nil {
		log.Println("auto update disabled")
		return nil
	}
	return err
}

func isAutoUpdateOn() (bool, error) {
	_, err := os.Stat(autoUpdateCronPath)
	if os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}
