//go:build windows

package health

import "fmt"

type syscallStatfs struct{}

var statfsFunc = func(path string, buf *syscallStatfs) error {
	return fmt.Errorf("disk check not supported on Windows")
}
