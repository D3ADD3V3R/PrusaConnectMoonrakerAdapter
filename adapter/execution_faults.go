package moonraker

import (
	"github.com/prusa3d/prusa-connect/sdk/fault"
	"net/http"
)

var executionDomain = func() fault.Domain {
	domain, err := fault.NewDomain("local.pcma.aux.moonraker")
	if err != nil {
		panic(err)
	}
	return domain
}()

var startBlocked = fault.Register(executionDomain, "START_BLOCKED", http.StatusConflict, fault.Document(
	"The execution cannot start with the current artifact, reservation or bed state",
	"Check the verified artifact and local bed confirmation for the previous execution.",
	fault.RetryNever, fault.ShowDetail,
))

var startUncertain = fault.Register(executionDomain, "START_UNCERTAIN", http.StatusConflict, fault.Document(
	"The printer did not conclusively acknowledge the execution start",
	"Reconcile the execution identity and printer state before requesting another execution.",
	fault.RetryNever, fault.ShowDetail,
))
