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
	"io/ioutil"
	"log"
	"os/exec"
	"runtime"

	arg "github.com/alexflint/go-arg"
)

var version = "<not set>"

const saltUpdateFile = "/etc/cacophony/saltUpdate.json"

//Args app arguments
type Args struct {
	RunDbus            bool `arg:"--run-dbus" help:"Run the dbus service."`
	RandomDelayMinutes int  `arg:"--random-delay-minutes" help:"Delay update between 0 and given minutes."` //TODO
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
	// If no error then keep the background goroutines running.
	runtime.Goexit()
}

func runMain() error {
	args := procArgs()
	log.Printf("running version: %s", version)

	salt := &saltUpdater{}

	if args.RunDbus {
		return startService(salt)
	}

	return nil
}

type saltUpdater struct {
	Running           bool
	LastUpdateOut     string
	LastUpdateSuccess bool
	LastUpdateChannel string
}

func (s *saltUpdater) runUpdate() {
	if s.Running {
		return
	}
	go func(s *saltUpdater) {
		log.Println("starting salt update")
		s.Running = true
		out, err := exec.Command("salt-call", "test.ping").Output()
		s.Running = false
		s.LastUpdateSuccess = err == nil
		s.LastUpdateOut = string(out)
		s.LastUpdateChannel = "TODO"
		saltJSON, err := json.Marshal(*s)
		if err != nil {
			log.Printf("failed to marshal saltUpdater: %v\n", err)
			return
		}
		err = ioutil.WriteFile(saltUpdateFile, saltJSON, 0644)
		if err != nil {
			log.Printf("failed to save salt JSON to file: %v\n", err)
		}
	}(s)
}
