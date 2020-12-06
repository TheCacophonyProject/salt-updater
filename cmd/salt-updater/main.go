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
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os/exec"
	"runtime"
	"time"

	saltrequester "github.com/TheCacophonyProject/salt-updater"
	arg "github.com/alexflint/go-arg"
)

var version = "<not set>"

const saltUpdateFile = "/etc/cacophony/saltUpdate.json"

//Args app arguments
type Args struct {
	RunDbus            bool `arg:"--run-dbus" help:"Run the dbus service."`
	RandomDelayMinutes int  `arg:"--random-delay-minutes" help:"Delay update between 0 and given minutes."`
	Ping               bool `arg:"--ping" help:"Don't run a salt state.apply, just ping the salt server. Will not delay call."`
	State              bool `arg:"--state" help:"Print out the current state of the salt update"`
}

//Version return version of app
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

	minutes := rand.Intn(args.RandomDelayMinutes + 1)
	log.Printf("waiting %v minutes before running salt udpate\n", minutes)
	time.Sleep(time.Duration(minutes) * time.Minute)

	log.Println("calling salt update")
	return saltrequester.RunUpdate()
}

func runDbus() error {
	//Read in previoue state
	saltState := &saltrequester.SaltState{}
	data, err := ioutil.ReadFile(saltUpdateFile)
	if err != nil {
		log.Printf("error reading previous salt state: %v", err)
	} else if err := json.Unmarshal(data, saltState); err != nil {
		log.Printf("error loading previous salt state: %v", err)
	} else {
		log.Printf("Previous salt state: %+v", *saltState)
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

func (s *saltUpdater) runSaltCall(args []string) {
	if s.state.RunningUpdate {
		return
	}
	go func(s *saltUpdater) {
		log.Printf("starting salt call: %v", args)
		s.state.RunningUpdate = true
		s.state.RunningArgs = args
		out, err := exec.Command("salt-call", args...).Output()
		s.state.RunningUpdate = false
		s.state.RunningArgs = nil
		log.Println("finished salt call")
		s.state.LastCallSuccess = err == nil
		s.state.LastCallOut = string(out)
		s.state.LastCallChannel = "TODO" //TODO one of: pi-dev, pi-test, pi-prod
		s.state.LastCallArgs = args
		saltStateJSON, err := json.Marshal(*s.state)
		if err != nil {
			log.Printf("failed to marshal saltUpdater: %v\n", err)
			return
		}
		err = ioutil.WriteFile(saltUpdateFile, saltStateJSON, 0644)
		if err != nil {
			log.Printf("failed to save salt JSON to file: %v\n", err)
		}
	}(s)
}
