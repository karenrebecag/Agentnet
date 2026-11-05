//go:build !unix

package expose

import "errors"

var errUnixOnly = errors.New("tunnel process control requires unix")

func startProcess(string) (Process, error) {
	return nil, errUnixOnly
}
