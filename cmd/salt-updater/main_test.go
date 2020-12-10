package main

import (
	"testing"

	saltrequester "github.com/TheCacophonyProject/salt-updater"
	"github.com/stretchr/testify/assert"
)

const testOutSuccess = `local:
Name: systemctl restart stay-on - Function: cmd.run - Result: Changed Started: - 15:14:07.884464 Duration: 79.173 ms
Name: echo dev-pis > /etc/cacophony/salt-nodegroup - Function: cmd.run - Result: Changed Started: - 15:14:18.582478 Duration: 28.601 ms
Name: date --iso-8601=seconds > /etc/cacophony/last-salt-update - Function: cmd.run - Result: Changed Started: - 15:14:19.684477 Duration: 31.971 ms
Name: version-reporter - Function: cmd.run - Result: Changed Started: - 15:14:19.717545 Duration: 113.323 ms
Name: systemctl stop stay-on - Function: cmd.run - Result: Changed Started: - 15:14:19.832504 Duration: 75.117 ms

Summary for local
--------------
Succeeded: 106 (changed=5)
Failed:      0
--------------
Total states run:     106
Total run time:    10.457 s`

const testOutFail = `local:
Name: systemctl restart stay-on - Function: cmd.run - Result: Changed Started: - 15:14:07.884464 Duration: 79.173 ms
Name: echo dev-pis > /etc/cacophony/salt-nodegroup - Function: cmd.run - Result: Changed Started: - 15:14:18.582478 Duration: 28.601 ms
Name: date --iso-8601=seconds > /etc/cacophony/last-salt-update - Function: cmd.run - Result: Changed Started: - 15:14:19.684477 Duration: 31.971 ms
Name: version-reporter - Function: cmd.run - Result: Changed Started: - 15:14:19.717545 Duration: 113.323 ms
Name: systemctl stop stay-on - Function: cmd.run - Result: Changed Started: - 15:14:19.832504 Duration: 75.117 ms

Summary for local
--------------
Succeeded: 106 (changed=5)
Failed:      1
--------------
Total states run:     106
Total run time:    10.457 s`

func TestMakeEvent(t *testing.T) {

	args := []string{"arg1", "arg2"}
	nodegroup := "test nodegroup"

	event, err := makeEventFromState(saltrequester.SaltState{
		LastCallSuccess:   true,
		LastCallArgs:      args,
		LastCallNodegroup: nodegroup,
		LastCallOut:       testOutSuccess,
	})
	assert.NoError(t, err)
	assert.Equal(t, event.Details["changed"], float64(5))
	assert.Equal(t, event.Details["succeeded"], float64(106))
	assert.Equal(t, event.Details["failed"], float64(0))
	assert.Equal(t, event.Details["nodegroup"], nodegroup)
	assert.Equal(t, event.Details["args"], args)
	assert.Equal(t, event.Details["out"], nil)
	assert.Equal(t, event.Details["runTime"], nil)

	event, err = makeEventFromState(saltrequester.SaltState{
		LastCallSuccess:   true,
		LastCallArgs:      args,
		LastCallNodegroup: nodegroup,
		LastCallOut:       testOutFail,
	})
	assert.NoError(t, err)
	assert.Equal(t, event.Details["changed"], float64(5))
	assert.Equal(t, event.Details["succeeded"], float64(106))
	assert.Equal(t, event.Details["failed"], float64(1))
	assert.Equal(t, event.Details["nodegroup"], nodegroup)
	assert.Equal(t, event.Details["args"], args)
	assert.Equal(t, event.Details["out"], testOutFail)
	assert.Equal(t, event.Details["runTime"], float64(10.457))
}
